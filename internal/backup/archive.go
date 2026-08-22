// Package backup validates and identifies persistent workload archives.
package backup

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"hash"
	"io"
	"math"
	"path"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

const (
	maximumArchiveEntries = 1_000_000
	maximumPathBytes      = 4096
	semanticVersion       = 1
	tarBlockBytes         = 512
	tarTerminatorBytes    = tarBlockBytes * 2
	tarTypeRegularLegacy  = byte(0)
)

var (
	// ErrInvalidArchive reports an archive that cannot be restored safely.
	ErrInvalidArchive = errors.New("persistent workload archive is invalid")
	// ErrArchiveLimit reports an archive that exceeds its caller-provided bound.
	ErrArchiveLimit = errors.New("persistent workload archive exceeds its limit")
)

// Inventory is the bounded content identity of one runtime archive stream.
type Inventory struct {
	EntryCount     int64
	PayloadBytes   int64
	ArchiveBytes   int64
	ArchiveDigest  domain.Digest
	SemanticDigest domain.Digest
}

type entryKind byte

const (
	entryRegular entryKind = iota + 1
	entryDirectory
	entrySymlink
	entryHardlink
)

type semanticEntry struct {
	name   string
	digest domain.Digest
}

type archiveScan struct {
	entries       []semanticEntry
	types         map[string]entryKind
	aliases       map[string]string
	ancestors     map[string]struct{}
	payloadBytes  int64
	caseFolder    cases.Caser
	contentHasher hash.Hash
}

type boundedArchiveReader struct {
	cancelled func() error
	source    io.Reader
	hasher    hash.Hash
	limit     int64
	count     int64
	exceeded  bool
	tail      [tarTerminatorBytes]byte
	tailSize  int
	tailNext  int
}

// Analyze consumes exactly one uncompressed tar stream and returns its byte
// and semantic identities. The caller must provide a positive maximum size.
func Analyze(ctx context.Context, source io.Reader, maximumBytes int64) (Inventory, error) {
	if source == nil || maximumBytes <= 0 {
		return Inventory{}, ErrInvalidArchive
	}
	reader := &boundedArchiveReader{
		cancelled: ctx.Err, source: source, hasher: sha256.New(), limit: maximumBytes,
	}
	scan := newArchiveScan()

	err := scanArchive(ctx, tar.NewReader(reader), &scan)
	if err != nil {
		return Inventory{}, err
	}
	if err = consumeZeroPadding(reader); err != nil {
		return Inventory{}, err
	}
	if !reader.completeTar() {
		return Inventory{}, ErrInvalidArchive
	}

	return scan.inventory(reader), nil
}

func newArchiveScan() archiveScan {
	return archiveScan{
		entries:       make([]semanticEntry, 0),
		types:         make(map[string]entryKind),
		aliases:       make(map[string]string),
		ancestors:     make(map[string]struct{}),
		caseFolder:    cases.Fold(),
		contentHasher: sha256.New(),
	}
}

func scanArchive(ctx context.Context, reader *tar.Reader, scan *archiveScan) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("scan persistent workload archive: %w", err)
		}

		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return classifyArchiveRead(err)
		}
		if err = scan.record(header, reader); err != nil {
			return err
		}
	}
}

func consumeZeroPadding(reader io.Reader) error {
	_, err := io.Copy(zeroPaddingWriter{}, reader)
	if err != nil {
		return classifyArchiveRead(err)
	}

	return nil
}

type zeroPaddingWriter struct{}

func (zeroPaddingWriter) Write(value []byte) (int, error) {
	for _, current := range value {
		if current != 0 {
			return 0, ErrInvalidArchive
		}
	}

	return len(value), nil
}

func (scan *archiveScan) inventory(reader *boundedArchiveReader) Inventory {
	slices.SortFunc(scan.entries, func(left, right semanticEntry) int {
		return strings.Compare(left.name, right.name)
	})
	semantic := sha256.New()
	_, _ = semantic.Write([]byte{semanticVersion})
	for _, entry := range scan.entries {
		appendFramed(semantic, []byte(entry.name))
		_, _ = semantic.Write(entry.digest[:])
	}

	return Inventory{
		EntryCount:     int64(len(scan.entries)),
		PayloadBytes:   scan.payloadBytes,
		ArchiveBytes:   reader.count,
		ArchiveDigest:  digestFromHash(reader.hasher),
		SemanticDigest: digestFromHash(semantic),
	}
}

// SameContent reports whether two inventories prove the same restorable path,
// metadata, link, and regular-file payload set.
func SameContent(left, right Inventory) bool {
	return validInventory(left) && validInventory(right) &&
		left.EntryCount == right.EntryCount &&
		left.PayloadBytes == right.PayloadBytes &&
		left.SemanticDigest == right.SemanticDigest
}

func (scan *archiveScan) record(header *tar.Header, body io.Reader) error {
	if header == nil {
		return ErrInvalidArchive
	}
	if len(scan.entries) >= maximumArchiveEntries {
		return ErrArchiveLimit
	}

	name, valid := normalizedMemberPath(header.Name, header.Typeflag == tar.TypeDir)
	if !valid || !validHeaderMetadata(header) || !scan.recordPath(name) {
		return ErrInvalidArchive
	}

	kind, link, valid := scan.entryIdentity(name, header)
	if !valid {
		return ErrInvalidArchive
	}
	if kind != entryRegular && header.Size != 0 {
		return ErrInvalidArchive
	}

	payloadDigest, err := scan.recordPayload(kind, header.Size, body)
	if err != nil {
		return err
	}

	scan.types[name] = kind
	scan.entries = append(scan.entries, semanticEntry{
		name:   name,
		digest: semanticEntryDigest(kind, header, link, payloadDigest),
	})

	return nil
}

func (scan *archiveScan) recordPayload(
	kind entryKind,
	size int64,
	body io.Reader,
) (domain.Digest, error) {
	if kind != entryRegular {
		return domain.Digest{}, nil
	}
	if size < 0 || scan.payloadBytes > math.MaxInt64-size {
		return domain.Digest{}, ErrArchiveLimit
	}

	scan.contentHasher.Reset()
	written, err := io.CopyN(scan.contentHasher, body, size)
	if err != nil || written != size {
		return domain.Digest{}, classifyArchiveRead(err)
	}
	scan.payloadBytes += size

	return digestFromHash(scan.contentHasher), nil
}

func (scan *archiveScan) recordPath(name string) bool {
	alias := scan.caseFolder.String(norm.NFC.String(name))
	if _, found := scan.aliases[alias]; found {
		return false
	}
	if _, hasDescendant := scan.ancestors[name]; hasDescendant {
		return false
	}

	for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
		if kind, found := scan.types[parent]; found && kind != entryDirectory {
			return false
		}
		scan.ancestors[parent] = struct{}{}
	}
	scan.aliases[alias] = name

	return true
}

func (scan *archiveScan) entryIdentity(name string, header *tar.Header) (entryKind, string, bool) {
	switch header.Typeflag {
	case tar.TypeReg, tarTypeRegularLegacy:
		return entryRegular, "", header.Linkname == ""
	case tar.TypeDir:
		return entryDirectory, "", header.Linkname == ""
	case tar.TypeSymlink:
		link, valid := normalizedSymlink(name, header.Linkname)

		return entrySymlink, link, valid
	case tar.TypeLink:
		link, valid := normalizedMemberPath(header.Linkname, false)
		if !valid || scan.types[link] != entryRegular {
			return 0, "", false
		}

		return entryHardlink, link, true
	default:
		return 0, "", false
	}
}

func normalizedMemberPath(value string, directory bool) (string, bool) {
	if directory {
		value = strings.TrimSuffix(value, "/")
	}
	if !validArchivePathText(value) || !canonicalMemberPath(value) {
		return "", false
	}

	return value, true
}

func normalizedSymlink(name, value string) (string, bool) {
	if !validArchivePathText(value) {
		return "", false
	}
	resolved := path.Clean(path.Join(path.Dir(name), value))
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return "", false
	}

	return value, true
}

func validArchivePathText(value string) bool {
	return value != "" && len(value) <= maximumPathBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0) && !strings.Contains(value, "\\") &&
		!strings.HasPrefix(value, "/")
}

func canonicalMemberPath(value string) bool {
	return path.Clean(value) == value && value != "." && value != ".." &&
		!strings.HasPrefix(value, "../")
}

func validHeaderMetadata(header *tar.Header) bool {
	if !validUnixMetadata(header) || !validOwnerName(header.Uname) || !validOwnerName(header.Gname) {
		return false
	}

	for key, value := range header.PAXRecords {
		if !validPAXRecord(key, value) {
			return false
		}
	}

	return header.Devmajor == 0 && header.Devminor == 0
}

func validUnixMetadata(header *tar.Header) bool {
	return header.Mode >= 0 && header.Mode <= 0o7777 && header.Uid >= 0 && header.Gid >= 0
}

func validOwnerName(value string) bool {
	return len(value) <= maximumPathBytes && utf8.ValidString(value)
}

func validPAXRecord(key, value string) bool {
	return key != "" && value != "" && len(key) <= maximumPathBytes &&
		len(value) <= maximumPathBytes && utf8.ValidString(key) && utf8.ValidString(value) &&
		!strings.Contains(strings.ToLower(key), "sparse")
}

func semanticEntryDigest(
	kind entryKind,
	header *tar.Header,
	link string,
	payload domain.Digest,
) domain.Digest {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte{byte(kind)})
	var encoded [binary.MaxVarintLen64]byte
	count := binary.PutVarint(encoded[:], header.Mode)
	_, _ = hasher.Write(encoded[:count])
	count = binary.PutVarint(encoded[:], int64(header.Uid))
	_, _ = hasher.Write(encoded[:count])
	count = binary.PutVarint(encoded[:], int64(header.Gid))
	_, _ = hasher.Write(encoded[:count])
	count = binary.PutVarint(encoded[:], header.ModTime.UTC().Unix())
	_, _ = hasher.Write(encoded[:count])
	count = binary.PutVarint(encoded[:], int64(header.ModTime.Nanosecond()))
	_, _ = hasher.Write(encoded[:count])
	appendFramed(hasher, []byte(header.Uname))
	appendFramed(hasher, []byte(header.Gname))
	appendFramed(hasher, []byte(link))
	_, _ = hasher.Write(payload[:])

	keys := make([]string, 0, len(header.PAXRecords))
	for key := range header.PAXRecords {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		appendFramed(hasher, []byte(key))
		appendFramed(hasher, []byte(header.PAXRecords[key]))
	}

	return digestFromHash(hasher)
}

func (reader *boundedArchiveReader) Read(destination []byte) (int, error) {
	if err := reader.cancelled(); err != nil {
		return 0, fmt.Errorf("read persistent workload archive: %w", err)
	}
	if reader.exceeded {
		return 0, ErrArchiveLimit
	}
	remaining := reader.limit - reader.count
	if remaining < 0 {
		return 0, ErrArchiveLimit
	}
	request := destination
	if remaining < int64(len(request)) {
		request = request[:remaining+1]
	}
	read, err := reader.source.Read(request)
	if read > 0 {
		_, _ = reader.hasher.Write(request[:read])
		reader.recordTail(request[:read])
		if int64(read) > remaining {
			reader.exceeded = true

			return read, ErrArchiveLimit
		}
		reader.count += int64(read)
	}

	if errors.Is(err, io.EOF) {
		return read, io.EOF
	}
	if err != nil {
		return read, fmt.Errorf("read persistent workload archive source: %w", err)
	}

	return read, nil
}

func (reader *boundedArchiveReader) recordTail(value []byte) {
	for _, current := range value {
		reader.tail[reader.tailNext] = current
		reader.tailNext = (reader.tailNext + 1) % len(reader.tail)
		if reader.tailSize < len(reader.tail) {
			reader.tailSize++
		}
	}
}

func (reader *boundedArchiveReader) completeTar() bool {
	return !reader.exceeded && reader.count >= tarTerminatorBytes && reader.count%tarBlockBytes == 0 &&
		reader.tailSize == len(reader.tail) && bytes.Equal(reader.tail[:], make([]byte, len(reader.tail)))
}

func classifyArchiveRead(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return ErrInvalidArchive
	}
	if errors.Is(err, ErrArchiveLimit) {
		return ErrArchiveLimit
	}

	return err
}

func validInventory(value Inventory) bool {
	return value.EntryCount >= 0 && value.EntryCount <= maximumArchiveEntries &&
		value.PayloadBytes >= 0 && value.PayloadBytes <= value.ArchiveBytes &&
		value.ArchiveBytes >= tarTerminatorBytes && value.ArchiveBytes%tarBlockBytes == 0 &&
		value.ArchiveDigest != (domain.Digest{}) && value.SemanticDigest != (domain.Digest{})
}

func appendFramed(destination hash.Hash, value []byte) {
	var encoded [binary.MaxVarintLen64]byte
	count := binary.PutUvarint(encoded[:], uint64(len(value)))
	_, _ = destination.Write(encoded[:count])
	_, _ = destination.Write(value)
}

func digestFromHash(hasher hash.Hash) domain.Digest {
	var digest domain.Digest
	copy(digest[:], hasher.Sum(nil))

	return digest
}
