package imagearchive

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageconfig"
)

const (
	testConfigMember       = "config"
	testLayerMember        = "layer"
	testPlainTextMediaType = "text/plain"
	testTaggedReference    = "x/a:1"
)

func TestStrictWireValidatorsRejectProtocolExtensions(t *testing.T) {
	t.Parallel()

	sha := "sha256:" + strings.Repeat("a", 64)
	testStrictDescriptorValidation(t, sha)
	testStrictEnvelopeValidation(t)
}

func testStrictDescriptorValidation(t *testing.T, sha string) {
	t.Helper()

	valid := descriptor{MediaType: ociManifestMediaType, Digest: sha, Size: 1}
	if !validImageDescriptor(valid, 1) {
		t.Fatal("valid image descriptor rejected")
	}
	for _, value := range []descriptor{
		{MediaType: ociManifestMediaType, Digest: sha, Size: 0},
		{MediaType: ociManifestMediaType, Digest: strings.ToUpper(sha), Size: 1},
		{MediaType: ociManifestMediaType, Digest: sha, Size: 1, URLs: []string{"https://example.invalid"}},
		{MediaType: ociManifestMediaType, Digest: sha, Size: 1, Data: []byte("embedded")},
		{MediaType: testPlainTextMediaType, Digest: sha, Size: 1},
	} {
		if validImageDescriptor(value, 1) {
			t.Fatalf("unsafe descriptor accepted: %#v", value)
		}
	}
}

func testStrictEnvelopeValidation(t *testing.T) {
	t.Helper()

	if validOCIIndexEnvelope(indexDocument{SchemaVersion: 1}) ||
		validOCIIndexEnvelope(indexDocument{SchemaVersion: 2, ArtifactType: "x"}) ||
		validOCIIndexEnvelope(indexDocument{SchemaVersion: 2, Subject: &descriptor{}}) ||
		validOCIIndexEnvelope(indexDocument{SchemaVersion: 2, MediaType: testPlainTextMediaType}) {
		t.Fatal("invalid OCI index envelope accepted")
	}
	if validOCIManifestEnvelope(manifestDocument{SchemaVersion: 1}) ||
		validOCIManifestEnvelope(manifestDocument{SchemaVersion: 2, ArtifactType: "x"}) ||
		validOCIManifestEnvelope(manifestDocument{SchemaVersion: 2, Subject: &descriptor{}}) ||
		validOCIManifestEnvelope(manifestDocument{SchemaVersion: 2, MediaType: testPlainTextMediaType}) {
		t.Fatal("invalid OCI manifest envelope accepted")
	}
}

func TestStrictDecodersAndSelectionBoundaries(t *testing.T) {
	t.Parallel()

	testStrictOCIDecoders(t)
	testStrictImageSelection(t)
	testStrictOCISelection(t)
	testStrictTagSelection(t)
}

func testStrictOCIDecoders(t *testing.T) {
	t.Helper()

	for _, raw := range [][]byte{
		{0xff}, []byte(`{"schemaVersion":2,"manifests":[]}`),
		[]byte(`{"schemaVersion":2,"manifests":[],"unknown":true}`),
	} {
		if _, err := decodeOCIIndex(raw); !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("decodeOCIIndex(%q) error = %v", raw, err)
		}
	}
	for _, raw := range [][]byte{
		{0xff}, []byte(`{"schemaVersion":2,"config":{},"layers":[]}`),
		[]byte(`{"schemaVersion":2,"config":{},"layers":[],"unknown":true}`),
	} {
		if _, err := decodeOCIManifest(raw); !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("decodeOCIManifest(%q) error = %v", raw, err)
		}
	}
}

func testStrictImageSelection(t *testing.T) {
	t.Helper()

	entries := []manifestEntry{{Config: "a", RepoTags: []string{"x/y:z"}}, {Config: "b", RepoTags: []string{"x/y:z"}}}
	if _, err := selectImage(entries, "x/y:z"); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("ambiguous tag error = %v", err)
	}
	entries[1].RepoTags = nil
	entries[1].Config = "a"
	if _, err := selectImage(entries, "@0"); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("duplicate config error = %v", err)
	}
	entries[1] = manifestEntry{Config: "b", RepoTags: []string{"x/other:z"}}
	if duplicateSelectedIdentity(entries, 0) {
		t.Fatal("distinct identity reported as duplicate")
	}
	if _, err := selectedManifestIndex(entries, "@2"); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("out-of-range index error = %v", err)
	}
}

func testStrictOCISelection(t *testing.T) {
	t.Helper()

	expected := domain.Platform{OS: linuxOperatingSystem, Architecture: amd64Architecture}
	descriptorValue := descriptor{MediaType: ociManifestMediaType, Digest: "sha256:" + strings.Repeat("b", 64),
		Size: 1, Platform: &platform{OS: linuxOperatingSystem, Architecture: amd64Architecture}}
	_, err := selectOCIDescriptor([]descriptor{descriptorValue, descriptorValue}, expected)
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("duplicate OCI descriptor error = %v", err)
	}
	descriptorValue.Platform.Architecture = arm64Architecture
	if _, err := selectOCIDescriptor([]descriptor{descriptorValue}, expected); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("missing platform error = %v", err)
	}

	second := descriptorValue
	second.Digest = "sha256:" + strings.Repeat("c", 64)
	second.Platform.Architecture = amd64Architecture
	descriptorValue.Platform.Architecture = amd64Architecture
	if _, err := selectOCIDescriptor([]descriptor{descriptorValue, second}, expected); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("duplicate platform error = %v", err)
	}
}

func testStrictTagSelection(t *testing.T) {
	t.Helper()

	if sources := taggedSources("/tmp/archive.tar::"); len(sources) != 0 {
		t.Fatalf("taggedSources(invalid reference) = %#v", sources)
	}
	if _, valid := normalizeArchiveTag(":"); valid {
		t.Fatal("normalizeArchiveTag(invalid reference) succeeded")
	}
	if index, err := selectedManifestIndex(
		[]manifestEntry{{RepoTags: []string{testTaggedReference}}}, "x/b:1",
	); err != nil || index != -1 {
		t.Fatalf("selectedManifestIndex(missing tag) = %d, %v", index, err)
	}
}

func TestArchiveMemberAndPlatformBoundaries(t *testing.T) {
	t.Parallel()

	testArchiveMemberBoundaries(t)
	testArchivePlatformBoundaries(t)
	testArchiveLayerBoundaries(t)
}

func testArchiveMemberBoundaries(t *testing.T) {
	t.Helper()

	if validMemberHeader(nil) || validMemberHeader(&tar.Header{Name: "x", Format: tar.FormatUSTAR, Size: -1}) ||
		validMemberHeader(&tar.Header{Name: "x", Format: tar.FormatUSTAR, Typeflag: tar.TypeDir, Size: 1}) {
		t.Fatal("invalid tar header accepted")
	}
	link := &tar.Header{Name: strings.Repeat("a", 64) + "/layer.tar", Format: tar.FormatUSTAR,
		Typeflag: tar.TypeSymlink, Linkname: "../" + strings.Repeat("b", 64) + ".tar"}
	if !validMemberHeader(link) {
		t.Fatal("legacy layer link rejected")
	}
	members := map[string]member{"link": {kind: tar.TypeSymlink, link: "../missing.tar"}}
	if validLayerLinks(members) {
		t.Fatal("dangling layer link accepted")
	}
}

func testArchivePlatformBoundaries(t *testing.T) {
	t.Helper()

	for _, evidence := range []imageconfig.Evidence{
		{Platform: domain.Platform{OS: "windows", Architecture: amd64Architecture}},
		{Platform: domain.Platform{OS: linuxOperatingSystem, Architecture: amd64Architecture, Variant: "v2"}},
		{Platform: domain.Platform{OS: linuxOperatingSystem, Architecture: arm64Architecture}},
		{Platform: domain.Platform{OS: linuxOperatingSystem, Architecture: "386"}},
		{Platform: domain.Platform{OS: linuxOperatingSystem, Architecture: amd64Architecture}, OSVersion: "1"},
	} {
		if supportedPlatform(evidence) {
			t.Fatalf("unsupported platform accepted: %#v", evidence)
		}
	}
}

func testArchiveLayerBoundaries(t *testing.T) {
	t.Helper()

	validLayer := descriptor{Digest: "sha256:" + strings.Repeat("c", 64), Size: 1}
	for _, mediaType := range []string{ociLayerMediaType, ociGzipLayerMediaType, ociZstdLayerMediaType,
		ociForeignLayerType, ociForeignGzipLayerType, ociForeignZstdLayerType,
		dockerLayerMediaType, dockerGzipLayerType, dockerForeignLayerType} {
		validLayer.MediaType = mediaType
		if !validOCILayerDescriptor(validLayer) {
			t.Fatalf("supported layer media type rejected: %s", mediaType)
		}
	}
	validLayer.MediaType = testPlainTextMediaType
	if validOCILayerDescriptor(validLayer) {
		t.Fatal("unsupported layer media type accepted")
	}
	err := validateOCILayers(context.Background(), nil, nil, nil, []layerIdentity{{}})
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("layer count mismatch error = %v", err)
	}
	err = validateOCILayers(
		context.Background(), nil, nil, []descriptor{validLayer}, []layerIdentity{{size: 1}},
	)
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("invalid layer descriptor error = %v", err)
	}
}

type errorReader struct{ err error }

func (reader errorReader) Read([]byte) (int, error) { return 0, reader.err }

type fileInfoWithoutSystem struct{ os.FileInfo }

func (fileInfoWithoutSystem) Sys() any { return nil }

func TestIOAndCancellationFailuresRemainClassified(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	reader := contextReader{check: ctx.Err, reader: bytes.NewReader(nil)}
	if _, err := reader.Read(make([]byte, 1)); !errors.Is(err, context.Canceled) {
		t.Fatalf("contextReader error = %v", err)
	}
	if err := archiveError(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("archiveError() = %v", err)
	}

	file, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = readMember(context.Background(), file, member{size: 1}, 1); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("readMember(closed) error = %v", err)
	}
	if err = writeStreamSpool(context.Background(), errorReader{io.ErrUnexpectedEOF}, file); err == nil {
		t.Fatal("writeStreamSpool accepted failed input/closed spool")
	}
	err = writeStreamSpool(context.Background(), bytes.NewReader(nil), mustTempFile(t))
	if !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("empty stream error = %v", err)
	}
}

func TestSourceIdentityAndArchiveIOBoundaries(t *testing.T) {
	t.Parallel()

	testExistingSourceBoundaries(t)
	testOpenedSourceBoundaries(t)
	testSourceOpenOperationFailures(t)
	testAnalyzeCloseFailure(t)
}

func testExistingSourceBoundaries(t *testing.T) {
	t.Helper()

	directory := t.TempDir()
	missing := filepath.Join(directory, "missing.tar")
	existing := filepath.Join(directory, "existing.tar")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	values := existingSources([]Source{{path: missing}, {path: existing}})
	if len(values) != 1 || values[0].path != existing {
		t.Fatalf("existingSources() = %#v", values)
	}
	if _, _, err := openSource(missing); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("openSource(missing) error = %v", err)
	}
	if _, valid := identity(nil); valid {
		t.Fatal("identity(nil) succeeded")
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, valid := identity(info); valid {
		t.Fatal("identity(directory) succeeded")
	}
	regular, err := os.Stat(existing)
	if err != nil {
		t.Fatal(err)
	}
	if _, valid := identity(fileInfoWithoutSystem{FileInfo: regular}); valid {
		t.Fatal("identity(file info without system metadata) succeeded")
	}
}

func testOpenedSourceBoundaries(t *testing.T) {
	t.Helper()

	existing := filepath.Join(t.TempDir(), "existing.tar")
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	file, before, err := openSource(existing)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(verifySourceIdentity(file, existing, before), ErrInvalidSource) {
		t.Fatal("verifySourceIdentity(closed) succeeded")
	}
	if _, _, err := scanArchive(context.Background(), file, before.size); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("scanArchive(closed) error = %v", err)
	}
}

func testSourceOpenOperationFailures(t *testing.T) {
	t.Helper()

	directory := t.TempDir()
	path := filepath.Join(directory, "archive.tar")
	other := filepath.Join(directory, "other.tar")
	if err := os.WriteFile(path, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("different"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	otherMetadata, err := os.Lstat(other)
	if err != nil {
		t.Fatal(err)
	}

	assertSourceOpenFailure(t, path, sourceOpenOperations{
		lstat: func(string) (os.FileInfo, error) { return fileInfoWithoutSystem{FileInfo: metadata}, nil },
	})
	assertSourceOpenFailure(t, path, sourceOpenOperations{
		lstat: os.Lstat,
		open:  func(string, int, uint32) (int, error) { return -1, os.ErrPermission },
	})
	assertSourceOpenFailure(t, path, sourceOpenOperations{
		lstat: os.Lstat,
		open:  syscall.Open,
		stat:  func(*os.File) (os.FileInfo, error) { return nil, os.ErrInvalid },
		close: func(file *os.File) error { return errors.Join(file.Close(), os.ErrClosed) },
	})
	assertSourceOpenFailure(t, path, sourceOpenOperations{
		lstat: os.Lstat,
		open:  syscall.Open,
		stat:  func(*os.File) (os.FileInfo, error) { return otherMetadata, nil },
		close: (*os.File).Close,
	})
}

func assertSourceOpenFailure(t *testing.T, path string, operations sourceOpenOperations) {
	t.Helper()
	if _, _, err := openSourceWithOperations(path, operations); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("openSourceWithOperations() error = %v", err)
	}
}

func testAnalyzeCloseFailure(t *testing.T) {
	t.Helper()

	path := writeMinimalInternalArchive(t)
	source := Source{path: path, selector: "@0", strictSingle: true}
	_, err := analyzeWithOperations(context.Background(), source, analyzeOperations{
		open: openSource,
		close: func(file *os.File) error {
			return errors.Join(file.Close(), os.ErrClosed)
		},
	})
	if !errors.Is(err, os.ErrClosed) {
		t.Fatalf("analyzeWithOperations(close failure) error = %v", err)
	}
}

func TestStrictManifestAndMemberBoundaries(t *testing.T) {
	t.Parallel()

	for _, raw := range [][]byte{
		[]byte(`[{"Config":"c","RepoTags":["busybox:1","docker.io/library/busybox:1"],"Layers":[]}]`),
		[]byte(`[{"Config":"c","RepoTags":["busybox"],"Layers":[]}]`),
		[]byte(`[{"Config":"../c","RepoTags":[],"Layers":[]}]`),
		[]byte(`[{"Config":"c","RepoTags":[],"Layers":["../l"]}]`),
	} {
		if _, err := decodeManifest(raw); !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("decodeManifest(%q) error = %v", raw, err)
		}
	}

	entries := []manifestEntry{
		{Config: "a", RepoTags: []string{testTaggedReference}},
		{Config: "b", RepoTags: []string{testTaggedReference}},
	}
	if !duplicateSelectedIdentity(entries, 0) {
		t.Fatal("duplicateSelectedIdentity() missed shared tag")
	}
	if selectedMembersExist(map[string]member{"c": {kind: tar.TypeReg, size: 0}}, manifestEntry{Config: "c"}) {
		t.Fatal("selectedMembersExist() accepted empty config")
	}

	hexName := strings.Repeat("a", 64)
	for _, header := range []*tar.Header{
		{Name: "short/layer.tar", Typeflag: tar.TypeSymlink},
		{Name: hexName + "/other.tar", Typeflag: tar.TypeSymlink, Linkname: "../" + hexName + ".tar"},
	} {
		if validLayerLink(header) {
			t.Fatalf("validLayerLink(%#v) succeeded", header)
		}
	}
}

func TestReadAndHashBoundaries(t *testing.T) {
	t.Parallel()

	testReadAndHashFailures(t)
	testFinalizeAnalysisFailures(t)
	testAnalyzeSelectedImageFailure(t)
}

func testReadAndHashFailures(t *testing.T) {
	t.Helper()

	file := mustBytesFile(t, []byte("payload"))
	defer closeTestFile(t, file)

	for _, value := range []member{{offset: -1, size: 1}, {offset: 0, size: -1}, {offset: 0, size: 2}} {
		if _, err := readMember(context.Background(), file, value, 1); !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("readMember(%#v) error = %v", value, err)
		}
	}
	closed := mustBytesFile(t, []byte("payload"))
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := hashMember(context.Background(), closed, member{size: 1}); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("hashMember(closed) error = %v", err)
	}
	if _, err := hashArchive(context.Background(), closed, 1); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("hashArchive(closed) error = %v", err)
	}
	entry := manifestEntry{Config: testConfigMember, Layers: []string{testLayerMember}}
	members := map[string]member{
		testConfigMember: {offset: 0, size: 1, kind: tar.TypeReg},
		testLayerMember:  {offset: 1, size: 1, kind: tar.TypeReg},
	}
	if _, _, err := readSelected(context.Background(), closed, members, entry); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("readSelected(closed config) error = %v", err)
	}
	if _, _, err := readSelected(context.Background(), file, map[string]member{
		testConfigMember: {offset: 0, size: 1, kind: tar.TypeReg},
		testLayerMember:  {offset: 100, size: 1, kind: tar.TypeReg},
	}, entry); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("readSelected(truncated layer) error = %v", err)
	}
}

func testFinalizeAnalysisFailures(t *testing.T) {
	t.Helper()

	closed := mustBytesFile(t, []byte("payload"))
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}

	resolved := resolvedArchiveImage{
		configDigest: domain.Hash([]byte(testConfigMember)), manifestDigest: domain.Hash([]byte("manifest")),
		platformManifest: domain.Hash([]byte("platform")), selected: selectedImage{},
		config: imageconfig.Evidence{Platform: domain.Platform{
			OS: linuxOperatingSystem, Architecture: amd64Architecture,
		}},
	}
	if _, err := finalizeAnalysis(
		context.Background(), closed, fileIdentity{size: 1}, Source{path: "invalid"}, resolved,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("finalizeAnalysis(closed) error = %v", err)
	}
	identityFile := mustBytesFile(t, []byte("x"))
	defer closeTestFile(t, identityFile)
	if _, err := finalizeAnalysis(
		context.Background(), identityFile, fileIdentity{size: 1}, Source{path: "invalid"}, resolved,
	); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("finalizeAnalysis(identity drift) error = %v", err)
	}
}

func testAnalyzeSelectedImageFailure(t *testing.T) {
	t.Helper()

	file := mustBytesFile(t, []byte("payload"))
	defer closeTestFile(t, file)
	members := map[string]member{testConfigMember: {offset: 0, size: 1, kind: tar.TypeReg}}
	entries := []manifestEntry{{Config: testConfigMember}}

	if _, _, _, _, err := analyzeSelectedImage(
		context.Background(), file, members, entries, "@0",
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("analyzeSelectedImage(invalid config) error = %v", err)
	}
	closed := mustBytesFile(t, []byte("payload"))
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := analyzeSelectedImage(
		context.Background(), closed, members, entries, "@0",
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("analyzeSelectedImage(read failure) error = %v", err)
	}
}

func TestOCIPayloadAndLayerBoundaries(t *testing.T) {
	t.Parallel()

	raw := []byte("payload")
	file := mustBytesFile(t, raw)
	defer closeTestFile(t, file)
	digest := domain.Hash(raw)
	value := descriptor{MediaType: ociLayerMediaType, Digest: digest.String(), Size: int64(len(raw))}
	members := map[string]member{
		"blobs/sha256/" + strings.TrimPrefix(digest.String(), "sha256:"): {
			offset: 0, size: int64(len(raw)), kind: tar.TypeReg,
		},
	}
	if payload, err := descriptorPayload(context.Background(), file, members, value, maximumArchiveBytes); err != nil ||
		!bytes.Equal(payload, raw) {
		t.Fatalf("descriptorPayload() = %q, %v", payload, err)
	}
	if _, err := descriptorPayload(
		context.Background(), file, nil, value, maximumArchiveBytes,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("descriptorPayload(missing) error = %v", err)
	}
	wrong := value
	wrong.Digest = domain.Hash([]byte("different")).String()
	digestMember := "blobs/sha256/" + strings.TrimPrefix(digest.String(), "sha256:")
	wrongMembers := map[string]member{
		"blobs/sha256/" + strings.TrimPrefix(wrong.Digest, "sha256:"): members[digestMember],
	}
	if _, err := descriptorPayload(
		context.Background(), file, wrongMembers, wrong, maximumArchiveBytes,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("descriptorPayload(digest mismatch) error = %v", err)
	}
	closed := mustBytesFile(t, raw)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := descriptorPayload(
		context.Background(), closed, members, value, maximumArchiveBytes,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("descriptorPayload(closed) error = %v", err)
	}

	selected := []layerIdentity{{size: int64(len(raw)), digest: digest}, {size: int64(len(raw)), digest: digest}}
	if err := validateOCILayers(
		context.Background(), file, members, []descriptor{value, value}, selected,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("validateOCILayers(duplicate) error = %v", err)
	}
	if err := validateOCILayers(
		context.Background(), file, nil, []descriptor{value}, selected[:1],
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("validateOCILayers(missing payload) error = %v", err)
	}
	invalid := value
	invalid.Size = 0
	if validOCILayerDescriptor(invalid) {
		t.Fatal("validOCILayerDescriptor(invalid descriptor) succeeded")
	}
}

func TestDecoderSizeAndRenderBoundaries(t *testing.T) {
	t.Parallel()

	digestValue := "sha256:" + strings.Repeat("a", 64)
	raw := []byte(`{"schemaVersion":2,"config":{"mediaType":"` + ociConfigMediaType +
		`","digest":"` + digestValue + `","size":1},"layers":[` +
		strings.Repeat("{},", maximumImageLayerCount) + `{}` + `]}`)
	if _, err := decodeOCIManifest(raw); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("decodeOCIManifest(too many layers) error = %v", err)
	}
	if _, err := decodeOCIIndex([]byte(`{"schemaVersion":1,"manifests":[{}]}`)); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("decodeOCIIndex(invalid envelope) error = %v", err)
	}

	digest := domain.Hash([]byte("identity"))
	analysis := Analysis{
		Source: Source{selector: "@0"}, ArchiveDigest: digest, ArchiveSize: 1,
		ManifestDigest: digest, ComposeReference: "example.com/image:1",
		Identity: domain.ImageIdentity{
			Origin: domain.ImageOriginDockerArchive, Reference: "example.com/other:1", ReferenceDigest: digest,
			Platform:         domain.Platform{OS: linuxOperatingSystem, Architecture: amd64Architecture},
			PlatformManifest: digest, ImageConfig: digest,
		},
	}
	if _, err := analysis.ServiceName("service"); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("ServiceName(reference drift) error = %v", err)
	}
}

func TestAnalyzeStreamContainsInputFailure(t *testing.T) {
	t.Parallel()

	if _, err := AnalyzeStream(context.Background(), errorReader{io.ErrUnexpectedEOF}); err == nil {
		t.Fatal("AnalyzeStream(error reader) succeeded")
	}
}

func TestAnalyzeStreamContainsSpoolCreationFailure(t *testing.T) {
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
	if _, err := AnalyzeStream(context.Background(), bytes.NewReader([]byte("archive"))); err == nil {
		t.Fatal("AnalyzeStream(unavailable temporary directory) succeeded")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeSpool(filepath.Join(file, "child")); err == nil {
		t.Fatal("removeSpool(non-directory parent) succeeded")
	}
}

func TestCreateStreamSpoolContainsOpenFailures(t *testing.T) {
	t.Parallel()

	t.Run("open root", func(t *testing.T) {
		t.Parallel()

		_, err := createStreamSpoolWithMkdirTemp(func(string, string) (string, error) {
			directory := filepath.Join(t.TempDir(), "removed")
			if err := os.Mkdir(directory, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(directory); err != nil {
				t.Fatal(err)
			}

			return directory, nil
		})
		if err == nil {
			t.Fatal("createStreamSpoolWithMkdirTemp(open root failure) succeeded")
		}
	})

	t.Run("create file", func(t *testing.T) {
		t.Parallel()

		_, err := createStreamSpoolWithMkdirTemp(func(string, string) (string, error) {
			directory := t.TempDir()
			path := filepath.Join(directory, savedArchiveName)
			if err := os.WriteFile(path, []byte("existing"), 0o600); err != nil {
				t.Fatal(err)
			}

			return directory, nil
		})
		if err == nil {
			t.Fatal("createStreamSpoolWithMkdirTemp(create file failure) succeeded")
		}
	})
}

func TestArchiveReaderAndRemainderFailures(t *testing.T) {
	t.Parallel()

	file := mustBytesFile(t, make([]byte, tarRecordBytes))
	defer closeTestFile(t, file)
	if validTarRemainder(context.Background(), file, 0, tarRecordBytes+1) {
		t.Fatal("validTarRemainder(unaligned size) succeeded")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if validTarRemainder(ctx, file, 0, tarRecordBytes) {
		t.Fatal("validTarRemainder(cancelled) succeeded")
	}
	largeZeroFile := mustBytesFile(t, make([]byte, archiveReadBufferBytes+tarRecordBytes))
	defer closeTestFile(t, largeZeroFile)
	if !validTarRemainder(context.Background(), largeZeroFile, 0, archiveReadBufferBytes+tarRecordBytes) {
		t.Fatal("validTarRemainder(large zero remainder) failed")
	}

	header := &tar.Header{Name: manifestName, Format: tar.FormatUSTAR, Typeflag: tar.TypeDir}
	if _, err := recordArchiveMember(
		context.Background(), tar.NewReader(bytes.NewReader(nil)), 0, header, make(map[string]member), nil,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("recordArchiveMember(directory manifest) error = %v", err)
	}
	header.Typeflag = tar.TypeReg
	header.Size = 1
	if _, err := recordArchiveMember(
		context.Background(), tar.NewReader(errorReader{io.ErrUnexpectedEOF}), 0, header,
		make(map[string]member), nil,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("recordArchiveMember(read failure) error = %v", err)
	}

	invalidTar := mustBytesFile(t, bytes.Repeat([]byte{0xff}, tarRecordBytes))
	defer closeTestFile(t, invalidTar)
	if _, _, err := scanArchive(context.Background(), invalidTar, tarRecordBytes); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("scanArchive(invalid tar) error = %v", err)
	}
	if _, _, err := scanArchiveWithLimit(
		context.Background(), file, tarRecordBytes, 0,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("scanArchiveWithLimit(member limit) error = %v", err)
	}
}

func TestOCIIndexReadAndValidationFailures(t *testing.T) {
	t.Parallel()

	testOCIIndexReadFailures(t)
	testOCIManifestReadFailures(t)
	testValidateOCIIndexFailures(t, domain.Platform{OS: linuxOperatingSystem, Architecture: amd64Architecture})
}

func testOCIIndexReadFailures(t *testing.T) {
	t.Helper()

	platformValue := domain.Platform{OS: linuxOperatingSystem, Architecture: amd64Architecture}
	if _, _, found, err := readOCIPlatformManifest(
		context.Background(), nil, map[string]member{indexName: {kind: tar.TypeDir}}, platformValue,
	); !errors.Is(err, ErrInvalidArchive) || found {
		t.Fatalf("readOCIPlatformManifest(nonregular index) = %t, %v", found, err)
	}

	malformed := []byte("not-json")
	file := mustBytesFile(t, malformed)
	defer closeTestFile(t, file)
	indexMembers := map[string]member{indexName: {offset: 0, size: int64(len(malformed)), kind: tar.TypeReg}}
	if _, _, found, err := readOCIPlatformManifest(
		context.Background(), file, indexMembers, platformValue,
	); !errors.Is(err, ErrInvalidArchive) || found {
		t.Fatalf("readOCIPlatformManifest(malformed index) = %t, %v", found, err)
	}
}

func testOCIManifestReadFailures(t *testing.T) {
	t.Helper()

	platformValue := domain.Platform{OS: linuxOperatingSystem, Architecture: amd64Architecture}

	manifest := []byte("bad manifest")
	manifestDigest := domain.Hash(manifest)
	index := mustInternalJSON(t, indexDocument{
		SchemaVersion: ociSchemaVersion,
		Manifests: []descriptor{{
			MediaType: ociManifestMediaType, Digest: manifestDigest.String(), Size: int64(len(manifest)),
			Platform: &platform{OS: linuxOperatingSystem, Architecture: amd64Architecture},
		}},
	})
	combined := append(bytes.Clone(index), manifest...)
	file := mustBytesFile(t, combined)
	defer closeTestFile(t, file)
	manifestName := "blobs/sha256/" + strings.TrimPrefix(manifestDigest.String(), "sha256:")
	members := map[string]member{
		indexName:    {offset: 0, size: int64(len(index)), kind: tar.TypeReg},
		manifestName: {offset: int64(len(index)), size: int64(len(manifest)), kind: tar.TypeReg},
	}
	if _, _, found, err := readOCIPlatformManifest(
		context.Background(), file, members, platformValue,
	); !errors.Is(err, ErrInvalidArchive) || found {
		t.Fatalf("readOCIPlatformManifest(malformed manifest) = %t, %v", found, err)
	}
	if _, _, found, err := readOCIPlatformManifest(
		context.Background(), file,
		map[string]member{indexName: members[indexName]},
		platformValue,
	); !errors.Is(err, ErrInvalidArchive) || found {
		t.Fatalf("readOCIPlatformManifest(missing manifest) = %t, %v", found, err)
	}

	closed := mustBytesFile(t, index)
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, found, err := readOCIPlatformManifest(
		context.Background(), closed,
		map[string]member{indexName: {offset: 0, size: int64(len(index)), kind: tar.TypeReg}},
		platformValue,
	); !errors.Is(err, ErrInvalidArchive) || found {
		t.Fatalf("readOCIPlatformManifest(closed) = %t, %v", found, err)
	}
}

func testValidateOCIIndexFailures(t *testing.T, platformValue domain.Platform) {
	t.Helper()

	config := []byte("config")
	layer := []byte("layer")
	configDigest := domain.Hash(config)
	layerDigest := domain.Hash(layer)
	imageManifest := mustInternalJSON(t, manifestDocument{
		SchemaVersion: ociSchemaVersion,
		MediaType:     ociManifestMediaType,
		Config: descriptor{
			MediaType: ociConfigMediaType, Digest: configDigest.String(), Size: int64(len(config)),
		},
		Layers: []descriptor{{
			MediaType: ociLayerMediaType, Digest: layerDigest.String(), Size: int64(len(layer)),
		}},
	})
	manifestDigest := domain.Hash(imageManifest)
	index := mustInternalJSON(t, indexDocument{
		SchemaVersion: ociSchemaVersion,
		Manifests: []descriptor{{
			MediaType: ociManifestMediaType, Digest: manifestDigest.String(), Size: int64(len(imageManifest)),
			Platform: &platform{
				OS: platformValue.OS, Architecture: platformValue.Architecture, Variant: platformValue.Variant,
			},
		}},
	})
	raw := bytes.Join([][]byte{index, imageManifest, config, layer}, nil)
	file := mustBytesFile(t, raw)
	defer closeTestFile(t, file)
	offset := int64(0)
	members := make(map[string]member)
	for _, value := range []struct {
		name string
		raw  []byte
	}{
		{name: indexName, raw: index},
		{name: "blobs/sha256/" + strings.TrimPrefix(manifestDigest.String(), "sha256:"), raw: imageManifest},
		{name: "blobs/sha256/" + strings.TrimPrefix(configDigest.String(), "sha256:"), raw: config},
		{name: "blobs/sha256/" + strings.TrimPrefix(layerDigest.String(), "sha256:"), raw: layer},
	} {
		members[value.name] = member{offset: offset, size: int64(len(value.raw)), kind: tar.TypeReg}
		offset += int64(len(value.raw))
	}
	layers := []layerIdentity{{size: int64(len(layer)), digest: layerDigest}}
	if _, _, err := validateOCIIndex(
		context.Background(), file, members, []byte("different config"), layers, platformValue,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("validateOCIIndex(config mismatch) error = %v", err)
	}
	if _, _, err := validateOCIIndex(
		context.Background(), file, members, config,
		[]layerIdentity{{size: int64(len(layer)), digest: domain.Hash([]byte("different layer"))}},
		platformValue,
	); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("validateOCIIndex(layer mismatch) error = %v", err)
	}
}

func mustInternalJSON(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func mustBytesFile(t *testing.T, raw []byte) *os.File {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "archive")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write(raw); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	return file
}

func writeMinimalInternalArchive(t *testing.T) string {
	t.Helper()

	file, err := os.CreateTemp(t.TempDir(), "archive-*.tar")
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewWriter(file)
	config := []byte(
		`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`,
	)
	manifest := []byte(`[{"Config":"config","RepoTags":["x/a:1"],"Layers":[]}]`)
	for _, value := range []struct {
		name string
		raw  []byte
	}{
		{name: manifestName, raw: manifest},
		{name: testConfigMember, raw: config},
	} {
		header := &tar.Header{
			Name: value.name, Mode: 0o600, Size: int64(len(value.raw)),
			Format: tar.FormatUSTAR, Typeflag: tar.TypeReg,
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write(value.raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	return file.Name()
}

func mustTempFile(t *testing.T) *os.File {
	t.Helper()
	file, err := os.CreateTemp(t.TempDir(), "spool")
	if err != nil {
		t.Fatal(err)
	}

	return file
}

func closeTestFile(t *testing.T, file *os.File) {
	t.Helper()
	if err := file.Close(); err != nil {
		t.Errorf("close test file: %v", err)
	}
}
