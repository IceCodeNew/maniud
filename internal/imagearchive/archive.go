// Package imagearchive validates one selected legacy Docker archive image
// without extracting or importing it.
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
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageconfig"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	sourcePrefix            = "docker-archive:"
	manifestName            = "manifest.json"
	indexName               = "index.json"
	maximumSourceBytes      = 32 << 10
	maximumManifestBytes    = int64(1 << 20)
	maximumConfiguration    = int64(16 << 20)
	maximumArchiveMembers   = 1_000_000
	maximumArchiveBytes     = int64(1 << 40)
	maximumImageLayerCount  = 1 << 16
	archiveReadBufferBytes  = 32 << 10
	tarRecordBytes          = 512
	archiveRepository       = "localhost/maniud/archive"
	linuxOperatingSystem    = "linux"
	amd64Architecture       = "amd64"
	arm64Architecture       = "arm64"
	ociSchemaVersion        = 2
	dockerManifestMediaType = "application/vnd.docker.distribution.manifest.v2+json"
	ociManifestMediaType    = "application/vnd.oci.image.manifest.v1+json"
	dockerIndexMediaType    = "application/vnd.docker.distribution.manifest.list.v2+json"
	ociIndexMediaType       = "application/vnd.oci.image.index.v1+json"
	dockerConfigMediaType   = "application/vnd.docker.container.image.v1+json"
	ociConfigMediaType      = "application/vnd.oci.image.config.v1+json"
	ociLayerMediaType       = "application/vnd.oci.image.layer.v1.tar"
	ociGzipLayerMediaType   = "application/vnd.oci.image.layer.v1.tar+gzip"
	ociZstdLayerMediaType   = "application/vnd.oci.image.layer.v1.tar+zstd"
	ociForeignLayerType     = "application/vnd.oci.image.layer.nondistributable.v1.tar"
	ociForeignGzipLayerType = "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip"
	ociForeignZstdLayerType = "application/vnd.oci.image.layer.nondistributable.v1.tar+zstd"
	dockerLayerMediaType    = "application/vnd.docker.image.rootfs.diff.tar"
	dockerGzipLayerType     = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	dockerForeignLayerType  = "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip"
)

var (
	// ErrInvalidSource reports invalid source syntax or unsafe filesystem identity.
	ErrInvalidSource = errors.New("docker archive source is invalid")
	// ErrInvalidArchive reports an unsupported or ambiguous archive structure.
	ErrInvalidArchive = errors.New("docker archive is invalid")
	hexNamePattern    = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hexLayerPattern   = regexp.MustCompile(`^[0-9a-f]{64}\.tar$`)
)

// Source is one absolute Docker archive path and exact member selector.
type Source struct {
	path         string
	selector     string
	strictSingle bool
}

// Analysis is the immutable image identity obtained from one archive member.
type Analysis struct {
	Source           Source
	ArchiveDigest    domain.Digest
	ArchiveSize      int64
	ManifestDigest   domain.Digest
	MemberIndex      int
	SourceReference  string
	ComposeReference string
	Identity         domain.ImageIdentity
}

type fileIdentity struct {
	device     uint64
	inode      uint64
	size       int64
	mode       os.FileMode
	modifiedNS int64
	changedNS  int64
}

type analyzeOperations struct {
	open  func(string) (*os.File, fileIdentity, error)
	close func(*os.File) error
}

type sourceOpenOperations struct {
	lstat func(string) (os.FileInfo, error)
	open  func(string, int, uint32) (int, error)
	stat  func(*os.File) (os.FileInfo, error)
	close func(*os.File) error
}

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

//nolint:tagliatelle // Docker defines these legacy manifest field names.
type manifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

type selectedImage struct {
	entry manifestEntry
	tags  []string
	index int
}

type layerIdentity struct {
	size   int64
	digest domain.Digest
}

type resolvedArchiveImage struct {
	configDigest     domain.Digest
	manifestDigest   domain.Digest
	platformManifest domain.Digest
	selected         selectedImage
	config           imageconfig.Evidence
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	URLs        []string          `json:"urls,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Data        []byte            `json:"data,omitempty"`
	Platform    *platform         `json:"platform,omitempty"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type platform struct {
	Architecture string   `json:"architecture"`
	OS           string   `json:"os"`
	OSVersion    string   `json:"os.version,omitempty"`
	OSFeatures   []string `json:"os.features,omitempty"`
	Variant      string   `json:"variant,omitempty"`
	Features     []string `json:"features,omitempty"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type indexDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Manifests     []descriptor      `json:"manifests"`
	Subject       *descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type manifestDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Subject       *descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

//nolint:tagliatelle // OCI defines these wire-field names.
type identityManifest struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	Config        identityDescriptor   `json:"config"`
	Layers        []identityDescriptor `json:"layers"`
}

//nolint:tagliatelle // OCI defines these wire-field names.
type identityDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// ParseSource parses docker-archive:PATH:TAG and docker-archive:PATH@INDEX.
func ParseSource(value string) (Source, error) {
	var empty Source
	if len(value) <= len(sourcePrefix) || len(value) > maximumSourceBytes || !strings.HasPrefix(value, sourcePrefix) ||
		!utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0 || strings.ContainsAny(value, "\r\n") {
		return empty, ErrInvalidSource
	}

	raw := strings.TrimPrefix(value, sourcePrefix)
	if source, valid := indexedSource(raw); valid {
		return source, nil
	}

	candidates := taggedSources(raw)
	if len(candidates) != 1 {
		candidates = existingSources(candidates)
	}
	if len(candidates) != 1 {
		return empty, ErrInvalidSource
	}

	return candidates[0], nil
}

func indexedSource(value string) (Source, bool) {
	separator := strings.LastIndexByte(value, '@')
	if separator <= 0 || separator == len(value)-1 {
		return Source{}, false
	}
	pathValue, indexValue := value[:separator], value[separator+1:]
	if strings.Trim(indexValue, "0123456789") != "" {
		return Source{}, false
	}

	pathValue = strings.TrimSuffix(pathValue, ":")
	index, err := strconv.ParseUint(indexValue, 10, 32)
	if err != nil || index >= maximumArchiveMembers || !validAbsolutePath(pathValue) {
		return Source{}, false
	}

	return Source{path: filepath.Clean(pathValue), selector: "@" + strconv.FormatUint(index, 10)}, true
}

func taggedSources(value string) []Source {
	result := make([]Source, 0, 1)
	for index := range len(value) {
		if value[index] != ':' {
			continue
		}

		pathValue, selector := value[:index], value[index+1:]
		if !validAbsolutePath(pathValue) || !explicitTaggedSelector(selector) {
			continue
		}

		normalized, err := imageref.Normalize(selector)
		if err != nil {
			continue
		}

		result = append(result, Source{path: filepath.Clean(pathValue), selector: normalized.String()})
	}

	return result
}

func existingSources(values []Source) []Source {
	result := make([]Source, 0, 1)
	for _, value := range values {
		if _, err := os.Lstat(value.path); err == nil {
			result = append(result, value)
		}
	}

	return result
}

func explicitTaggedSelector(value string) bool {
	return value != "" && !strings.ContainsRune(value, '@') &&
		strings.LastIndexByte(value, ':') > strings.LastIndexByte(value, '/')
}

func validAbsolutePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value &&
		strings.IndexByte(value, 0) < 0
}

// Path returns the absolute operator-owned archive path.
func (source Source) Path() string {
	return source.path
}

// Selector returns the canonical tagged reference or source index.
func (source Source) Selector() string {
	return source.selector
}

// Analyze validates and resolves one selected archive member without extraction.
func Analyze(ctx context.Context, source Source) (Analysis, error) {
	return analyzeWithOperations(ctx, source, analyzeOperations{
		open:  openSource,
		close: (*os.File).Close,
	})
}

func analyzeWithOperations(
	ctx context.Context,
	source Source,
	operations analyzeOperations,
) (Analysis, error) {
	var empty Analysis
	if err := ctx.Err(); err != nil {
		return empty, fmt.Errorf("analyze docker archive: %w", err)
	}
	if !validAbsolutePath(source.path) || !validSelector(source.selector) {
		return empty, ErrInvalidSource
	}

	file, before, err := operations.open(source.path)
	if err != nil {
		return empty, err
	}
	analysis, analyzeErr := analyzeOpenArchive(ctx, file, before, source)
	closeErr := operations.close(file)
	if analyzeErr != nil {
		return empty, errors.Join(analyzeErr, closeErr)
	}
	if closeErr != nil {
		return empty, fmt.Errorf("close docker archive: %w", closeErr)
	}

	return analysis, nil
}

func analyzeOpenArchive(
	ctx context.Context,
	file *os.File,
	before fileIdentity,
	source Source,
) (Analysis, error) {
	var empty Analysis

	members, manifest, err := scanArchive(ctx, file, before.size)
	if err != nil {
		return empty, err
	}
	entries, err := decodeManifest(manifest)
	if err != nil {
		return empty, err
	}
	if source.strictSingle && len(entries) != 1 {
		return empty, ErrInvalidArchive
	}
	selected, config, layers, configEvidence, err := analyzeSelectedImage(
		ctx, file, members, entries, source.selector,
	)
	if err != nil {
		return empty, err
	}
	platformManifest, hasOCIManifest, err := validateOCIIndex(
		ctx,
		file,
		members,
		config,
		layers,
		configEvidence.Platform,
	)
	if err != nil {
		return empty, err
	}

	resolved := resolveArchiveImage(
		selected,
		config,
		layers,
		configEvidence,
		platformManifest,
		hasOCIManifest,
	)

	return finalizeAnalysis(ctx, file, before, source, resolved)
}

func resolveArchiveImage(
	selected selectedImage,
	config []byte,
	layers []layerIdentity,
	configEvidence imageconfig.Evidence,
	platformManifest domain.Digest,
	hasOCIManifest bool,
) resolvedArchiveImage {
	configDigest := domain.Hash(config)
	manifestDigest := archiveManifestDigest(configDigest, int64(len(config)), layers)
	if !hasOCIManifest {
		platformManifest = manifestDigest
	}

	return resolvedArchiveImage{
		configDigest:     configDigest,
		manifestDigest:   manifestDigest,
		platformManifest: platformManifest,
		selected:         selected,
		config:           configEvidence,
	}
}

func finalizeAnalysis(
	ctx context.Context,
	file *os.File,
	before fileIdentity,
	source Source,
	resolved resolvedArchiveImage,
) (Analysis, error) {
	var empty Analysis
	archiveDigest, err := hashArchive(ctx, file, before.size)
	if err != nil {
		return empty, err
	}
	sourceReference := firstTag(resolved.selected.tags)
	composeReference := archiveComposeReference(source.selector, sourceReference, resolved.manifestDigest)

	if err := verifySourceIdentity(file, source.path, before); err != nil {
		return empty, err
	}

	return Analysis{
		Source:           source,
		ArchiveDigest:    archiveDigest,
		ArchiveSize:      before.size,
		ManifestDigest:   resolved.manifestDigest,
		MemberIndex:      resolved.selected.index,
		SourceReference:  sourceReference,
		ComposeReference: composeReference,
		Identity: resolved.config.Identity(domain.ImageIdentity{
			Origin:           domain.ImageOriginDockerArchive,
			Reference:        composeReference,
			ReferenceDigest:  resolved.manifestDigest,
			Platform:         resolved.config.Platform,
			PlatformManifest: resolved.platformManifest,
			ImageConfig:      resolved.configDigest,
		}),
	}, nil
}

func analyzeSelectedImage(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	entries []manifestEntry,
	selector string,
) (selectedImage, []byte, []layerIdentity, imageconfig.Evidence, error) {
	selected, err := selectImage(entries, selector)
	if err != nil || !selectedMembersExist(members, selected.entry) {
		return selectedImage{}, nil, nil, imageconfig.Evidence{}, ErrInvalidArchive
	}

	config, layers, err := readSelected(ctx, file, members, selected.entry)
	if err != nil {
		return selectedImage{}, nil, nil, imageconfig.Evidence{}, err
	}
	configEvidence, err := imageconfig.Decode(config, maximumConfiguration)
	if err != nil || !supportedPlatform(configEvidence) {
		return selectedImage{}, nil, nil, imageconfig.Evidence{}, ErrInvalidArchive
	}

	return selected, config, layers, configEvidence, nil
}

func firstTag(tags []string) string {
	if len(tags) == 0 {
		return ""
	}

	return tags[0]
}

func validSelector(value string) bool {
	if index, found := strings.CutPrefix(value, "@"); found {
		_, err := strconv.ParseUint(index, 10, 32)

		return err == nil
	}

	return explicitTaggedSelector(value)
}

func openSource(name string) (*os.File, fileIdentity, error) {
	return openSourceWithOperations(name, sourceOpenOperations{
		lstat: os.Lstat,
		open:  syscall.Open,
		stat:  (*os.File).Stat,
		close: (*os.File).Close,
	})
}

func openSourceWithOperations(
	name string,
	operations sourceOpenOperations,
) (*os.File, fileIdentity, error) {
	var empty fileIdentity
	metadata, err := operations.lstat(name)
	if err != nil || !metadata.Mode().IsRegular() || metadata.Size() <= 0 || metadata.Size() > maximumArchiveBytes {
		return nil, empty, ErrInvalidSource
	}
	before, valid := identity(metadata)
	if !valid {
		return nil, empty, ErrInvalidSource
	}

	descriptor, err := operations.open(
		name,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, empty, ErrInvalidSource
	}
	file := os.NewFile(uintptr(descriptor), filepath.Base(name))
	opened, err := operations.stat(file)
	openedIdentity, valid := identity(opened)
	if err != nil || !valid || openedIdentity != before {
		closeErr := operations.close(file)

		return nil, empty, errors.Join(ErrInvalidSource, closeErr)
	}

	return file, before, nil
}

func identity(info os.FileInfo) (fileIdentity, bool) {
	if info == nil || !info.Mode().IsRegular() {
		return fileIdentity{}, false
	}
	value, valid := info.Sys().(*syscall.Stat_t)
	if !valid {
		return fileIdentity{}, false
	}

	return fileIdentity{
		device:     statDevice(value),
		inode:      statInode(value),
		size:       info.Size(),
		mode:       info.Mode(),
		modifiedNS: info.ModTime().UnixNano(),
		changedNS:  statChangeTime(value),
	}, true
}

func verifySourceIdentity(file *os.File, name string, before fileIdentity) error {
	opened, err := file.Stat()
	openedIdentity, openedValid := identity(opened)
	current, pathErr := os.Lstat(name)
	currentIdentity, currentValid := identity(current)
	if err != nil || pathErr != nil || !openedValid || !currentValid ||
		openedIdentity != before || currentIdentity != before {
		return ErrInvalidSource
	}

	return nil
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
		if count >= memberLimit {
			return nil, nil, ErrInvalidArchive
		}
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, archiveError(ctx)
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
	if header == nil || header.Format != tar.FormatUSTAR || !canonicalMemberName(header.Name) {
		return false
	}
	if header.Size < 0 || len(header.PAXRecords) != 0 {
		return false
	}

	switch header.Typeflag {
	case tar.TypeReg:
		return true
	case tar.TypeDir:
		return header.Size == 0
	case tar.TypeSymlink:
		return validLayerLink(header)
	default:
		return false
	}
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

func decodeManifest(raw []byte) ([]manifestEntry, error) {
	var entries []manifestEntry
	if !utf8.Valid(raw) || !jsonstrict.Decode(bytes.NewReader(raw), maximumManifestBytes, &entries) ||
		len(entries) == 0 || len(entries) > maximumArchiveMembers {
		return nil, ErrInvalidArchive
	}

	for index := range entries {
		if !normalizeManifestEntry(&entries[index]) {
			return nil, ErrInvalidArchive
		}
	}

	return entries, nil
}

func normalizeManifestEntry(entry *manifestEntry) bool {
	if !canonicalMemberName(entry.Config) || len(entry.Layers) > maximumImageLayerCount ||
		!uniqueCanonicalMembers(entry.Layers) {
		return false
	}

	normalized := make([]string, len(entry.RepoTags))
	seen := make(map[string]struct{}, len(entry.RepoTags))
	for index, tag := range entry.RepoTags {
		canonical, valid := normalizeArchiveTag(tag)
		if !valid {
			return false
		}
		if _, duplicate := seen[canonical]; duplicate {
			return false
		}
		seen[canonical] = struct{}{}
		normalized[index] = canonical
	}
	entry.RepoTags = normalized

	return true
}

func normalizeArchiveTag(tag string) (string, bool) {
	if !explicitTaggedSelector(tag) {
		return "", false
	}
	source, err := imageref.Normalize(tag)
	if err != nil {
		return "", false
	}

	return source.String(), true
}

func uniqueCanonicalMembers(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !canonicalMemberName(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}

	return true
}

func selectImage(entries []manifestEntry, selector string) (selectedImage, error) {
	selectedIndex, err := selectedManifestIndex(entries, selector)
	if err != nil {
		return selectedImage{}, err
	}
	if selectedIndex < 0 || duplicateSelectedIdentity(entries, selectedIndex) {
		return selectedImage{}, ErrInvalidArchive
	}

	return selectedImage{
		entry: entries[selectedIndex], tags: entries[selectedIndex].RepoTags, index: selectedIndex,
	}, nil
}

func selectedManifestIndex(entries []manifestEntry, selector string) (int, error) {
	if selected, found := strings.CutPrefix(selector, "@"); found {
		index, err := strconv.Atoi(selected)
		if err != nil || index < 0 || index >= len(entries) {
			return -1, ErrInvalidArchive
		}

		return index, nil
	}

	selectedIndex := -1
	for index, entry := range entries {
		if !contains(entry.RepoTags, selector) {
			continue
		}
		if selectedIndex >= 0 {
			return -1, ErrInvalidArchive
		}
		selectedIndex = index
	}

	return selectedIndex, nil
}

func duplicateSelectedIdentity(entries []manifestEntry, selected int) bool {
	config := entries[selected].Config
	tags := make(map[string]struct{}, len(entries[selected].RepoTags))
	for _, tag := range entries[selected].RepoTags {
		tags[tag] = struct{}{}
	}
	for index, entry := range entries {
		if index == selected {
			continue
		}
		if entry.Config == config {
			return true
		}
		for _, tag := range entry.RepoTags {
			if _, found := tags[tag]; found {
				return true
			}
		}
	}

	return false
}

func contains(values []string, expected string) bool {
	return slices.Contains(values, expected)
}

func selectedMembersExist(members map[string]member, entry manifestEntry) bool {
	config, found := members[entry.Config]
	if !found || config.kind != tar.TypeReg ||
		config.size <= 0 || config.size > maximumConfiguration {
		return false
	}
	for _, name := range entry.Layers {
		value, layerFound := resolvedMember(members, name)
		if !layerFound || value.kind != tar.TypeReg {
			return false
		}
	}

	return true
}

func resolvedMember(members map[string]member, name string) (member, bool) {
	value, found := members[name]
	if !found || value.kind != tar.TypeSymlink {
		return value, found
	}

	target := strings.TrimPrefix(value.link, "../")
	value, found = members[target]

	return value, found
}

func readSelected(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	entry manifestEntry,
) ([]byte, []layerIdentity, error) {
	config, err := readMember(ctx, file, members[entry.Config], maximumConfiguration)
	if err != nil {
		return nil, nil, err
	}
	layers := make([]layerIdentity, len(entry.Layers))
	for index, name := range entry.Layers {
		value, _ := resolvedMember(members, name)
		digest, err := hashMember(ctx, file, value)
		if err != nil {
			return nil, nil, err
		}
		layers[index] = layerIdentity{size: value.size, digest: digest}
	}

	return config, layers, nil
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

func supportedPlatform(value imageconfig.Evidence) bool {
	if value.OSVersion != "" || len(value.OSFeatures) != 0 || value.Platform.OS != linuxOperatingSystem {
		return false
	}

	switch value.Platform.Architecture {
	case amd64Architecture:
		return value.Platform.Variant == ""
	case arm64Architecture:
		return value.Platform.Variant == "v8"
	default:
		return false
	}
}
