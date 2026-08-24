package imagearchive_test

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
)

const (
	manifestMember        = "manifest.json"
	testArchiveTag        = "example.com/team/app:Latest"
	testArchitectureAMD64 = "amd64"
	testArchitectureARM64 = "arm64"
	mediaTypeKey          = "mediaType"
	digestKey             = "digest"
	sizeKey               = "size"
	diffIDsKey            = "diff_ids"
	architectureKey       = "architecture"
	osKey                 = "os"
	rootFSKey             = "rootfs"
	typeKey               = "type"
	configKey             = "config"
	layersType            = "layers"
	testOSLinux           = "linux"
)

type tarMember struct {
	name       string
	body       []byte
	kind       byte
	link       string
	format     tar.Format
	paxRecords map[string]string
}

type archiveFixture struct {
	path   string
	raw    []byte
	config []byte
	layer  []byte
}

type fixtureOptions struct {
	architecture   string
	variant        string
	withOCI        bool
	untagged       bool
	indexPlatform  string
	indexMediaType string
}

func writeArchive(t *testing.T, directory string, members []tarMember, trailing []byte) (string, []byte) {
	t.Helper()

	var output bytes.Buffer
	archive := tar.NewWriter(&output)
	for _, value := range members {
		kind := value.kind
		if kind == 0 {
			kind = tar.TypeReg
		}
		size := int64(len(value.body))
		if kind != tar.TypeReg {
			size = 0
		}
		header := &tar.Header{
			Name: value.name, Mode: 0o644, Size: size, Typeflag: kind,
			Linkname: value.link, Format: value.format, PAXRecords: value.paxRecords,
		}
		if err := archive.WriteHeader(header); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if size > 0 {
			if _, err := archive.Write(value.body); err != nil {
				t.Fatalf("Write() error = %v", err)
			}
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	output.Write(trailing)

	if directory == "" {
		directory = t.TempDir()
	}
	archivePath := filepath.Join(directory, "image 'quoted'.tar")
	if err := os.WriteFile(archivePath, output.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return archivePath, bytes.Clone(output.Bytes())
}

func newFixture(t *testing.T, options fixtureOptions) archiveFixture {
	t.Helper()

	layer := []byte("real layer bytes")
	config := mustJSON(t, map[string]any{
		architectureKey: options.architecture,
		osKey:           testOSLinux,
		"variant":       options.variant,
		rootFSKey: map[string]any{
			typeKey: layersType, diffIDsKey: []string{digest(layer)},
		},
		configKey: map[string]any{
			"Entrypoint": []string{"/init"},
			"Cmd":        []string{"serve"},
		},
	})
	tags := []string{testArchiveTag}
	if options.untagged {
		tags = nil
	}
	manifest := mustJSON(t, []map[string]any{{
		"Config": "config.json", "RepoTags": tags, "Layers": []string{"layer.tar"},
	}})
	members := []tarMember{
		{name: manifestMember, body: manifest},
		{name: "config.json", body: config},
		{name: "layer.tar", body: layer},
	}
	if options.withOCI {
		members = append(members, ociMembers(t, options, config, layer)...)
	}
	archivePath, raw := writeArchive(t, "", members, nil)

	return archiveFixture{path: archivePath, raw: raw, config: config, layer: layer}
}

func ociMembers(t *testing.T, options fixtureOptions, config, layer []byte) []tarMember {
	t.Helper()

	manifest := mustJSON(t, map[string]any{
		"schemaVersion": 2,
		mediaTypeKey:    "application/vnd.oci.image.manifest.v1+json",
		configKey: map[string]any{
			mediaTypeKey: "application/vnd.oci.image.config.v1+json",
			digestKey:    digest(config),
			sizeKey:      len(config),
		},
		"layers": []map[string]any{{
			mediaTypeKey: "application/vnd.oci.image.layer.v1.tar",
			digestKey:    digest(layer),
			sizeKey:      len(layer),
		}},
	})
	platformArchitecture := options.architecture
	if options.indexPlatform != "" {
		platformArchitecture = options.indexPlatform
	}
	manifestMediaType := "application/vnd.oci.image.manifest.v1+json"
	if options.indexMediaType != "" {
		manifestMediaType = options.indexMediaType
	}
	index := mustJSON(t, map[string]any{
		"schemaVersion": 2,
		"manifests": []map[string]any{{
			mediaTypeKey: manifestMediaType,
			digestKey:    digest(manifest),
			sizeKey:      len(manifest),
			"platform": map[string]any{
				osKey: testOSLinux, architectureKey: platformArchitecture, "variant": options.variant,
			},
		}},
	})

	return []tarMember{
		{name: "index.json", body: index},
		{name: blobName(manifest), body: manifest},
		{name: blobName(config), body: config},
		{name: blobName(layer), body: layer},
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}

	return raw
}

func digest(value []byte) string {
	hash := sha256.Sum256(value)

	return "sha256:" + hex.EncodeToString(hash[:])
}

func blobName(value []byte) string {
	return "blobs/sha256/" + strings.TrimPrefix(digest(value), "sha256:")
}

func TestAnalyzeTaggedAndIndexed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		architecture string
		variant      string
		selector     string
		withOCI      bool
	}{
		{name: "tagged OCI amd64", architecture: testArchitectureAMD64, selector: testArchiveTag, withOCI: true},
		{name: "indexed arm64", architecture: testArchitectureARM64, variant: "v8", selector: "@0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newFixture(t, fixtureOptions{
				architecture: test.architecture, variant: test.variant, withOCI: test.withOCI,
			})
			analysis := analyzeFixture(t, fixture, test.selector)
			assertAnalysis(t, analysis, fixture, test.architecture, test.selector, test.withOCI)
		})
	}
}

func TestImportCommandSupportsClassicAndContainerdImageStores(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		withOCI bool
	}{
		{name: "legacy archive", withOCI: false},
		{name: "OCI archive", withOCI: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newFixture(t, fixtureOptions{
				architecture: testArchitectureAMD64,
				withOCI:      test.withOCI,
				untagged:     true,
			})
			analysis := analyzeFixture(t, fixture, "@0")
			command := analysis.ImportCommand()
			for _, value := range []string{
				shellQuoted(fixture.path),
				shellQuoted("linux/amd64"),
				shellQuoted(analysis.Identity.PlatformManifest.String()),
				shellQuoted(analysis.ManifestDigest.String()),
				shellQuoted(analysis.Identity.ImageConfig.String()),
				shellQuoted(analysis.ComposeReference),
			} {
				if !strings.Contains(command, value) {
					t.Fatalf("ImportCommand() = %q, missing %q", command, value)
				}
			}
			want := 2
			if test.withOCI {
				want = 3
			}
			if got := strings.Count(command, "docker image tag "); got != want {
				t.Fatalf("ImportCommand() tag count = %d, want %d: %q", got, want, command)
			}
		})
	}
}

func TestAnalyzeStreamUsesTheStrictArchiveParser(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, fixtureOptions{architecture: testArchitectureAMD64, withOCI: true})
	analysis, err := imagearchive.AnalyzeStream(context.Background(), bytes.NewReader(fixture.raw))
	if err != nil {
		t.Fatalf("AnalyzeStream() error = %v", err)
	}
	if analysis.Source.Path() == fixture.path || analysis.Source.Selector() != "@0" ||
		analysis.Identity.ImageConfig.String() != digest(fixture.config) {
		t.Fatalf("AnalyzeStream() = %#v", analysis)
	}

	if _, err = imagearchive.AnalyzeStream(context.Background(), nil); !errors.Is(err, imagearchive.ErrInvalidArchive) {
		t.Fatalf("AnalyzeStream(nil) error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = imagearchive.AnalyzeStream(cancelled, bytes.NewReader(fixture.raw)); !errors.Is(err, context.Canceled) {
		t.Fatalf("AnalyzeStream(cancelled) error = %v", err)
	}
}

func TestAnalyzeStreamRejectsMultipleImages(t *testing.T) {
	t.Parallel()

	config := []byte(
		`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`,
	)
	manifest := []byte(`[` +
		`{"Config":"a","RepoTags":["x/a:1"],"Layers":[]},` +
		`{"Config":"b","RepoTags":["x/b:1"],"Layers":[]}]`)
	_, raw := writeArchive(t, "", []tarMember{
		{name: manifestMember, body: manifest},
		{name: "a", body: config},
		{name: "b", body: config},
	}, nil)
	if _, err := imagearchive.AnalyzeStream(
		context.Background(), bytes.NewReader(raw),
	); !errors.Is(err, imagearchive.ErrInvalidArchive) {
		t.Fatalf("AnalyzeStream(multiple images) error = %v", err)
	}
}

func TestAnalyzeRejectsRootFSDiffIDCountMismatch(t *testing.T) {
	t.Parallel()

	config := []byte(
		`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`,
	)
	manifest := []byte(`[{"Config":"config","RepoTags":["x/a:1"],"Layers":["layer"]}]`)
	archivePath, _ := writeArchive(t, "", []tarMember{
		{name: manifestMember, body: manifest},
		{name: "config", body: config},
		{name: "layer", body: []byte("layer")},
	}, nil)
	source, err := imagearchive.ParseSource("docker-archive:" + archivePath + ":x/a:1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = imagearchive.Analyze(context.Background(), source); !errors.Is(err, imagearchive.ErrInvalidArchive) {
		t.Fatalf("Analyze() error = %v, want ErrInvalidArchive", err)
	}
}

func analyzeFixture(t *testing.T, fixture archiveFixture, selector string) imagearchive.Analysis {
	t.Helper()

	separator := ":"
	if strings.HasPrefix(selector, "@") {
		separator = ""
	}
	source, err := imagearchive.ParseSource("docker-archive:" + fixture.path + separator + selector)
	if err != nil {
		t.Fatalf("ParseSource() error = %v", err)
	}
	analysis, err := imagearchive.Analyze(context.Background(), source)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}

	return analysis
}

func assertAnalysis(
	t *testing.T,
	analysis imagearchive.Analysis,
	fixture archiveFixture,
	architecture string,
	selector string,
	withOCI bool,
) {
	t.Helper()

	if analysis.Source.Path() != fixture.path || analysis.Source.Selector() != selector {
		t.Fatalf("source = %#v", analysis.Source)
	}
	assertArchiveEvidence(t, analysis, fixture)
	assertImageIdentity(t, analysis, fixture, architecture, withOCI)
	if analysis.SourceReference != testArchiveTag || analysis.ComposeReference != testArchiveTag {
		t.Fatalf("references = %q, %q", analysis.SourceReference, analysis.ComposeReference)
	}
	if !strings.Contains(analysis.ImportCommand(), shellQuoted(fixture.path)) {
		t.Fatalf("import command = %q", analysis.ImportCommand())
	}
}

func assertArchiveEvidence(t *testing.T, analysis imagearchive.Analysis, fixture archiveFixture) {
	t.Helper()

	if analysis.ArchiveDigest.String() != digest(fixture.raw) || analysis.ArchiveSize != int64(len(fixture.raw)) {
		t.Fatalf("archive evidence = %s, %d", analysis.ArchiveDigest, analysis.ArchiveSize)
	}
}

func assertImageIdentity(
	t *testing.T,
	analysis imagearchive.Analysis,
	fixture archiveFixture,
	architecture string,
	withOCI bool,
) {
	t.Helper()

	if analysis.Identity.Origin != domain.ImageOriginDockerArchive ||
		analysis.Identity.ImageConfig.String() != digest(fixture.config) ||
		analysis.Identity.Platform.Architecture != architecture ||
		analysis.Identity.ReferenceDigest != analysis.ManifestDigest ||
		(!withOCI && analysis.Identity.PlatformManifest != analysis.ManifestDigest) ||
		(withOCI && analysis.Identity.PlatformManifest == analysis.ManifestDigest) {
		t.Fatalf("image identity = %#v", analysis.Identity)
	}
}

func shellQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func TestParseSource(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, fixtureOptions{architecture: testArchitectureAMD64})
	atDirectory := filepath.Join(t.TempDir(), "path@with-at")
	if err := os.Mkdir(atDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	atPath, _ := writeArchive(t, atDirectory, []tarMember{
		{name: manifestMember, body: []byte(`[]`)},
	}, nil)

	tests := []struct {
		name     string
		value    string
		path     string
		selector string
		valid    bool
	}{
		{name: "tag", value: "docker-archive:" + fixture.path + ":busybox:Latest", path: fixture.path,
			selector: "docker.io/library/busybox:Latest", valid: true},
		{name: "index", value: "docker-archive:" + fixture.path + "@000", path: fixture.path,
			selector: "@0", valid: true},
		{name: "at in path", value: "docker-archive:" + atPath + "@0", path: atPath, selector: "@0", valid: true},
		{name: "wrong scheme", value: "x"},
		{name: "relative path", value: "docker-archive:relative:x:y"},
		{name: "bad index", value: "docker-archive:" + fixture.path + "@x"},
		{name: "large index", value: "docker-archive:" + fixture.path + "@1000000"},
		{name: "missing tag", value: "docker-archive:" + fixture.path + ":busybox"},
		{name: "digest selector", value: "docker-archive:" + fixture.path + ":busybox@sha256:" +
			strings.Repeat("a", 64)},
		{name: "control character", value: "docker-archive:" + fixture.path + "\n:x:y"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source, err := imagearchive.ParseSource(test.value)
			if (err == nil) != test.valid {
				t.Fatalf("ParseSource() error = %v", err)
			}
			if test.valid && (source.Path() != test.path || source.Selector() != test.selector) {
				t.Fatalf("ParseSource() = %#v", source)
			}
		})
	}
}

func TestAnalyzeRejectsUnsafeSourceAndCancellation(t *testing.T) {
	t.Parallel()

	fixture := newFixture(t, fixtureOptions{architecture: testArchitectureAMD64})
	source, err := imagearchive.ParseSource("docker-archive:" + fixture.path + "@0")
	if err != nil {
		t.Fatalf("ParseSource() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = imagearchive.Analyze(ctx, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("Analyze(cancelled) error = %v", err)
	}

	symlinkPath := fixture.path + ".link"
	if err = os.Symlink(fixture.path, symlinkPath); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	symlink, err := imagearchive.ParseSource("docker-archive:" + symlinkPath + "@0")
	if err != nil {
		t.Fatalf("ParseSource(symlink) error = %v", err)
	}
	if _, err = imagearchive.Analyze(context.Background(), symlink); !errors.Is(err, imagearchive.ErrInvalidSource) {
		t.Fatalf("Analyze(symlink) error = %v", err)
	}
	_, err = imagearchive.Analyze(context.Background(), imagearchive.Source{})
	if !errors.Is(err, imagearchive.ErrInvalidSource) {
		t.Fatalf("Analyze(empty) error = %v", err)
	}
}

func TestAnalyzeAcceptsLegacyLayerSymlink(t *testing.T) {
	t.Parallel()

	layer := []byte("layer")
	config := mustJSON(t, map[string]any{
		architectureKey: testArchitectureAMD64, osKey: testOSLinux,
		rootFSKey: map[string]any{typeKey: layersType, diffIDsKey: []string{digest(layer)}},
	})
	layerName := strings.Repeat("a", 64) + ".tar"
	legacyDirectory := strings.Repeat("b", 64)
	manifest := []byte(`[{"Config":"c","RepoTags":["x/y:z"],"Layers":["` +
		legacyDirectory + `/layer.tar"]}]`)
	archivePath, _ := writeArchive(t, "", []tarMember{
		{name: manifestMember, body: manifest},
		{name: "c", body: config},
		{name: layerName, body: layer},
		{name: legacyDirectory + "/layer.tar", kind: tar.TypeSymlink, link: "../" + layerName},
	}, nil)
	source, err := imagearchive.ParseSource("docker-archive:" + archivePath + ":x/y:z")
	if err != nil {
		t.Fatalf("ParseSource() error = %v", err)
	}
	if _, err = imagearchive.Analyze(context.Background(), source); err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
}

func TestAnalyzeRejectsMalformedArchives(t *testing.T) {
	t.Parallel()

	layer := []byte("x")
	config := mustJSON(t, map[string]any{
		architectureKey: testArchitectureAMD64, osKey: testOSLinux,
		rootFSKey: map[string]any{typeKey: layersType, diffIDsKey: []string{digest(layer)}},
	})
	manifest := []byte(`[{"Config":"c","RepoTags":["x/y:z"],"Layers":["l"]}]`)
	valid := []tarMember{
		{name: manifestMember, body: manifest}, {name: "c", body: config}, {name: "l", body: layer},
	}
	tests := []struct {
		name     string
		members  []tarMember
		trailing []byte
	}{
		{name: "missing manifest", members: []tarMember{{name: "c", body: config}}},
		{name: "duplicate member", members: append(bytesMembers(valid), tarMember{name: "c", body: config})},
		{name: "truncated manifest", members: []tarMember{{name: manifestMember, body: manifest[:3]}}},
		{name: "path traversal", members: []tarMember{{name: "../manifest.json", body: manifest}}},
		{name: "special member", members: []tarMember{{name: manifestMember, body: manifest},
			{name: "fifo", kind: tar.TypeFifo}}},
		{name: "pax metadata", members: []tarMember{{name: manifestMember, body: manifest,
			paxRecords: map[string]string{"comment": "unexpected"}}}},
		{name: "gnu format", members: []tarMember{{name: manifestMember, body: manifest, format: tar.FormatGNU}}},
		{name: "unknown manifest field", members: []tarMember{{name: manifestMember,
			body: []byte(`[{"Config":"c","RepoTags":["x/y:z"],"Layers":[],"X":1}]`)},
			{name: "c", body: config}}},
		{name: "duplicate manifest field", members: []tarMember{{name: manifestMember,
			body: []byte(`[{"Config":"c","Config":"c","RepoTags":["x/y:z"],"Layers":[]}]`)},
			{name: "c", body: config}}},
		{name: "missing selected member", members: valid[:2]},
		{name: "duplicate layer", members: []tarMember{{name: manifestMember,
			body: []byte(`[{"Config":"c","RepoTags":["x/y:z"],"Layers":["l","l"]}]`)},
			{name: "c", body: config}, {name: "l", body: []byte("x")}}},
		{name: "unsupported platform", members: []tarMember{{name: manifestMember, body: manifest},
			{name: "c", body: []byte(`{"architecture":"386","os":"linux"}`)},
			{name: "l", body: []byte("x")}}},
		{name: "nonzero trailing", members: valid, trailing: append(make([]byte, 511), 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			archivePath, _ := writeArchive(t, "", test.members, test.trailing)
			source, parseErr := imagearchive.ParseSource("docker-archive:" + archivePath + ":x/y:z")
			if parseErr != nil {
				t.Fatalf("ParseSource() error = %v", parseErr)
			}
			if _, err := imagearchive.Analyze(context.Background(), source); !errors.Is(err, imagearchive.ErrInvalidArchive) {
				t.Fatalf("Analyze() error = %v", err)
			}
		})
	}
}

func bytesMembers(values []tarMember) []tarMember {
	return append([]tarMember(nil), values...)
}

func TestOCIIndexMustProveSelectedPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options fixtureOptions
	}{
		{name: "platform mismatch", options: fixtureOptions{
			architecture: testArchitectureAMD64, withOCI: true, indexPlatform: testArchitectureARM64,
		}},
		{name: "unsupported media type", options: fixtureOptions{
			architecture: testArchitectureAMD64, withOCI: true, indexMediaType: "text/plain",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := newFixture(t, test.options)
			source, err := imagearchive.ParseSource("docker-archive:" + fixture.path + "@0")
			if err != nil {
				t.Fatalf("ParseSource() error = %v", err)
			}
			if _, err = imagearchive.Analyze(context.Background(), source); !errors.Is(err, imagearchive.ErrInvalidArchive) {
				t.Fatalf("Analyze() error = %v", err)
			}
		})
	}
}
