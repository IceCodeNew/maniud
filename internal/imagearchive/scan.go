package imagearchive

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
)

var (
	hexNamePattern  = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hexLayerPattern = regexp.MustCompile(`^[0-9a-f]{64}\.tar$`)
)

type member struct {
	offset int64
	size   int64
	kind   byte
	link   string
}

type countingReader struct {
	reader io.Reader
	count  int64
}

func (reader *countingReader) Read(value []byte) (int, error) {
	read, err := reader.reader.Read(value)
	reader.count += int64(read)

	return read, err //nolint:wrapcheck // Preserve io.EOF semantics for archive/tar.
}

type contextReader struct {
	check  func() error
	reader io.Reader
}

func (reader contextReader) Read(value []byte) (int, error) {
	if err := reader.check(); err != nil {
		return 0, fmt.Errorf("read docker archive: %w", err)
	}

	return reader.reader.Read(value) //nolint:wrapcheck // Preserve io.EOF semantics for archive/tar.
}

func scanArchive(
	ctx context.Context,
	file *os.File,
	size int64,
) (map[string]member, []byte, error) {
	return scanArchiveWithLimit(ctx, file, size, maximumArchiveMembers)
}

func scanArchiveWithLimit(
	ctx context.Context,
	file *os.File,
	size int64,
	memberLimit int,
) (map[string]member, []byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, ErrInvalidArchive
	}
	reader := &countingReader{reader: contextReader{check: ctx.Err, reader: file}}
	archive := tar.NewReader(reader)
	members := make(map[string]member)
	var manifest []byte

	for count := 0; ; count++ {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, archiveError(ctx)
		}
		if count >= memberLimit {
			return nil, nil, ErrInvalidArchive
		}
		manifest, err = recordArchiveMember(ctx, archive, reader.count, header, members, manifest)
		if err != nil {
			return nil, nil, err
		}
	}
	if len(manifest) == 0 || !validLayerLinks(members) || !validTarRemainder(ctx, file, reader.count, size) {
		return nil, nil, archiveError(ctx)
	}

	return members, manifest, nil
}

func recordArchiveMember(
	ctx context.Context,
	archive *tar.Reader,
	offset int64,
	header *tar.Header,
	members map[string]member,
	manifest []byte,
) ([]byte, error) {
	if !validMemberHeader(header) {
		return nil, ErrInvalidArchive
	}
	if _, duplicate := members[header.Name]; duplicate {
		return nil, ErrInvalidArchive
	}
	members[header.Name] = member{offset: offset, size: header.Size, kind: header.Typeflag, link: header.Linkname}
	if header.Name != manifestName {
		return manifest, nil
	}
	if header.Typeflag != tar.TypeReg || header.Size <= 0 || header.Size > maximumManifestBytes {
		return nil, ErrInvalidArchive
	}

	raw, err := io.ReadAll(io.LimitReader(archive, maximumManifestBytes+1))
	if err != nil || int64(len(raw)) != header.Size {
		return nil, archiveError(ctx)
	}

	return raw, nil
}

func validMemberHeader(header *tar.Header) bool {
	if header == nil || !validCommonMemberHeader(header) {
		return false
	}

	switch header.Typeflag {
	case tar.TypeReg:
		return canonicalMemberName(header.Name)
	case tar.TypeDir:
		return header.Size == 0 && canonicalDirectoryName(header.Name)
	case tar.TypeSymlink:
		return canonicalMemberName(header.Name) && validLayerLink(header)
	default:
		return false
	}
}

func validCommonMemberHeader(header *tar.Header) bool {
	return header.Format == tar.FormatUSTAR && header.Size >= 0 && len(header.PAXRecords) == 0
}

func canonicalDirectoryName(name string) bool {
	return canonicalMemberName(strings.TrimSuffix(name, "/"))
}

func validLayerLink(header *tar.Header) bool {
	parts := strings.Split(header.Name, "/")
	if header.Size != 0 || len(parts) != 2 || !hexNamePattern.MatchString(parts[0]) {
		return false
	}
	if parts[1] != "layer.tar" || !strings.HasPrefix(header.Linkname, "../") {
		return false
	}

	return hexLayerPattern.MatchString(strings.TrimPrefix(header.Linkname, "../"))
}

func canonicalMemberName(name string) bool {
	return name != "" && utf8.ValidString(name) && !strings.ContainsAny(name, "\\\x00\r\n") &&
		!strings.HasPrefix(name, "/") && path.Clean(name) == name && name != "." &&
		!strings.Contains(name, "../")
}

func validLayerLinks(members map[string]member) bool {
	for _, value := range members {
		if value.kind != tar.TypeSymlink {
			continue
		}
		target, found := members[strings.TrimPrefix(value.link, "../")]
		if !found || target.kind != tar.TypeReg {
			return false
		}
	}

	return true
}

func validTarRemainder(ctx context.Context, file *os.File, offset, size int64) bool {
	if offset > size || size%tarBlockSize() != 0 {
		return false
	}
	reader := contextReader{check: ctx.Err, reader: io.NewSectionReader(file, offset, size-offset)}
	buffer := make([]byte, archiveReadBufferBytes)
	for {
		read, err := reader.Read(buffer)
		if bytes.IndexFunc(buffer[:read], func(value rune) bool { return value != 0 }) >= 0 {
			return false
		}
		if errors.Is(err, io.EOF) {
			return true
		}
		if err != nil {
			return false
		}
	}
}

func tarBlockSize() int64 {
	return tarRecordBytes
}

func archiveError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("analyze docker archive: %w", err)
	}

	return ErrInvalidArchive
}

func readMember(ctx context.Context, file *os.File, value member, maximum int64) ([]byte, error) {
	if value.size < 0 || value.size > maximum || value.offset < 0 {
		return nil, ErrInvalidArchive
	}
	buffer := make([]byte, value.size)
	reader := contextReader{check: ctx.Err, reader: io.NewSectionReader(file, value.offset, value.size)}
	if _, err := io.ReadFull(reader, buffer); err != nil {
		return nil, archiveError(ctx)
	}

	return buffer, nil
}

func hashMember(ctx context.Context, file *os.File, value member) (domain.Digest, error) {
	hash := sha256.New()
	reader := contextReader{check: ctx.Err, reader: io.NewSectionReader(file, value.offset, value.size)}
	written, err := io.Copy(hash, reader)
	if err != nil || written != value.size {
		return domain.Digest{}, archiveError(ctx)
	}

	var digest domain.Digest
	copy(digest[:], hash.Sum(nil))

	return digest, nil
}

func hashArchive(ctx context.Context, file *os.File, size int64) (domain.Digest, error) {
	hash := sha256.New()
	reader := contextReader{check: ctx.Err, reader: io.NewSectionReader(file, 0, size)}
	written, err := io.Copy(hash, reader)
	if err != nil || written != size {
		return domain.Digest{}, archiveError(ctx)
	}

	var digest domain.Digest
	copy(digest[:], hash.Sum(nil))

	return digest, nil
}
