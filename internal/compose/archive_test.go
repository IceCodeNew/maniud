package compose

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testArchiveDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testArchiveConfig  = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testArchiveImage   = "example.com/team/archive:1"
	testInvalidArchive = "invalid"
	archiveServicesKey = "services"
)

func TestArchiveImageInputProjectsEmbeddedIdentity(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, archiveCompose()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	input, err := project.ImageInput("")
	if err != nil || input.Kind() != ImageInputDockerArchive {
		t.Fatalf("ImageInput() = %#v, %v", input, err)
	}
	identity, valid := input.ArchiveIdentity()
	if !validArchiveTestIdentity(identity, valid) {
		t.Fatalf("ArchiveIdentity() = %#v, %t", identity, valid)
	}
	if _, registry := input.RegistrySource(); registry {
		t.Fatal("RegistrySource() accepted archive input")
	}

	workload, err := project.Workload("", identity)
	if !validArchiveTestWorkload(workload, err) {
		t.Fatalf("Workload() = %#v, %v", workload, err)
	}
}

func TestArchiveWorkloadRejectsUnprovenImageConfiguration(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, archiveCompose()))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	input, err := project.ImageInput("")
	if err != nil {
		t.Fatalf("ImageInput() error = %v", err)
	}
	identity, valid := input.ArchiveIdentity()
	if !valid {
		t.Fatal("ArchiveIdentity() did not return archive evidence")
	}
	identity.User = "1000"
	if _, err := project.Workload("", identity); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Workload(unproven image config) error = %v", err)
	}
}

func TestArchiveImageInputProjectsUntaggedIndex(t *testing.T) {
	t.Parallel()

	synthetic := "localhost/maniud/archive:source-" + strings.TrimPrefix(testManifestDigest, "sha256:")
	content := strings.ReplaceAll(archiveCompose(), testArchiveImage, synthetic)
	content = strings.Replace(content, "selector: "+synthetic, `selector: "@0"`, 1)
	content = strings.Replace(content, "        source_reference: "+synthetic+"\n", "", 1)
	project, err := Load(context.Background(), testSource(t, content))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	input, err := project.ImageInput("")
	identity, valid := input.ArchiveIdentity()
	if err != nil || !valid || identity.Reference != synthetic {
		t.Fatalf("ImageInput(index) = %#v, %v", input, err)
	}
}

func TestArchiveDecoderRejectsMalformedShapes(t *testing.T) {
	t.Parallel()

	validMetadata := map[string]any{
		"kind": "docker-archive", "selector": "@0", "archive_digest": testArchiveDigest,
		"archive_size": 10240, "archive_manifest_digest": testManifestDigest,
		"archive_member_index": 0, archivePlatformField: "linux/amd64",
		"reference_digest": testManifestDigest, "platform_manifest_digest": testManifestDigest,
		"image_config_digest": testArchiveConfig,
	}
	for _, raw := range []map[string]any{
		{archiveExtensionKey: testInvalidArchive},
		{archiveExtensionKey: map[string]any{"wrong": map[string]any{}}},
		{archiveExtensionKey: map[string]any{archiveServicesKey: testInvalidArchive}},
		{archiveExtensionKey: map[string]any{archiveServicesKey: map[string]any{}}},
		{archiveExtensionKey: map[string]any{archiveServicesKey: map[string]any{apiService: testInvalidArchive}}},
		{archiveExtensionKey: map[string]any{archiveServicesKey: map[string]any{apiService: map[string]any{
			archiveImageSourceKey: testInvalidArchive,
		}}}},
	} {
		if _, _, _, valid := decodeManiudSources(
			map[string]any{archiveExtensionKey: raw[archiveExtensionKey]},
		); valid {
			t.Fatalf("decodeManiudSources(%#v) accepted malformed value", raw)
		}
	}

	unknown := maps.Clone(validMetadata)
	delete(unknown, "kind")
	unknown["unknown"] = "docker-archive"
	badSourceReference := maps.Clone(validMetadata)
	badSourceReference["source_reference"] = 1
	badDigest := maps.Clone(validMetadata)
	badDigest["archive_digest"] = 1
	for _, raw := range []map[string]any{unknown, badSourceReference, badDigest} {
		if _, valid := decodeArchiveImageSource(raw); valid {
			t.Fatalf("decodeArchiveImageSource(%#v) accepted malformed value", raw)
		}
	}
}

func TestArchiveDecoderHelperBoundaries(t *testing.T) {
	t.Parallel()

	if _, valid := exactMapping(testInvalidArchive, "key"); valid {
		t.Fatal("exactMapping accepted scalar")
	}
	if _, valid := exactMapping(map[string]any{"key": 1, testOtherValue: 2}, "key"); valid {
		t.Fatal("exactMapping accepted multiple fields")
	}
	if optionalString(map[string]any{"value": ""}, "value") != nil ||
		optionalString(map[string]any{"value": 1}, "value") != nil {
		t.Fatal("optionalString accepted invalid value")
	}
	if _, valid := digestValue(1); valid {
		t.Fatal("digestValue accepted non-string")
	}
	if validArchiveSelector("@00", 0, "") || validArchiveSelector("@0", 1, "") ||
		validOptionalSourceReference("busybox") {
		t.Fatal("archive selector accepted non-canonical value")
	}
	if _, valid := archivePlatform("linux/386"); valid {
		t.Fatal("archivePlatform accepted unsupported platform")
	}
}

func validArchiveTestIdentity(identity domain.ImageIdentity, valid bool) bool {
	return valid && identity.Origin == domain.ImageOriginDockerArchive && identity.Reference == testArchiveImage &&
		identity.ReferenceDigest.String() == testManifestDigest &&
		identity.PlatformManifest.String() == testManifestDigest &&
		identity.ImageConfig.String() == testArchiveConfig && identity.Platform == (domain.Platform{
		OS: archiveLinuxOS, Architecture: archiveAMD64, Variant: "",
	})
}

func validArchiveTestWorkload(workload domain.DesiredWorkload, err error) bool {
	return err == nil && workload.Image.Origin == domain.ImageOriginDockerArchive &&
		len(workload.Entrypoint) == 1 && workload.Entrypoint[0] == testInitEntrypoint &&
		len(workload.Command) == 1 && workload.Command[0] == testServeCommand
}

func TestArchiveComposeRejectsMetadataAndServiceDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "archive path disclosure", old: "        image_config_digest:",
			new: "        path: /private/image.tar\n        image_config_digest:"},
		{name: "wrong kind", old: "kind: docker-archive", new: "kind: registry"},
		{name: "wrong archive size", old: "archive_size: 10240", new: "archive_size: 0"},
		{name: "index selector mismatch", old: "selector: " + testArchiveImage, new: "selector: @1"},
		{name: "wrong metadata platform", old: "        platform: linux/amd64",
			new: "        platform: linux/arm64/v8"},
		{name: "wrong reference digest", old: "reference_digest: " + testManifestDigest,
			new: "reference_digest: " + testArchiveDigest},
		{name: "malformed platform digest", old: "platform_manifest_digest: " + testManifestDigest,
			new: "platform_manifest_digest: sha512:" + strings.Repeat("a", 64)},
		{name: "missing source reference", old: "        source_reference: " + testArchiveImage + "\n", new: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			content := strings.Replace(archiveCompose(), test.old, test.new, 1)
			project, err := Load(context.Background(), testSource(t, content))
			if err == nil {
				_, err = project.ImageInput("")
			}
			if !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("archive drift error = %v", err)
			}
		})
	}
}

func TestArchiveComposeRejectsRuntimeProjectionDrift(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "image", old: "image: " + testArchiveImage, new: "image: example.com/team/other:1"},
		{name: "platform", old: "platform: linux/amd64", new: "platform: linux/arm64/v8"},
		{name: "pull policy", old: "pull_policy: never", new: "pull_policy: always"},
		{name: "extension service", old: "    api:\n      image_source:", new: "    other:\n      image_source:"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			content := strings.Replace(archiveCompose(), test.old, test.new, 1)
			project, err := Load(context.Background(), testSource(t, content))
			if err == nil {
				_, err = project.ImageInput("")
			}
			if !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("runtime drift error = %v", err)
			}
		})
	}
}

func archiveCompose() string {
	return `
name: example
services:
  api:
    container_name: example-api
    image: ` + testArchiveImage + `
    network_mode: bridge
    platform: linux/amd64
    pull_policy: never
    entrypoint: ["/init"]
    command: ["serve"]
x-maniud:
  services:
    api:
      image_source:
        kind: docker-archive
        selector: ` + testArchiveImage + `
        archive_digest: ` + testArchiveDigest + `
        archive_size: 10240
        archive_manifest_digest: ` + testManifestDigest + `
        archive_member_index: 0
        platform: linux/amd64
        source_reference: ` + testArchiveImage + `
        reference_digest: ` + testManifestDigest + `
        platform_manifest_digest: ` + testManifestDigest + `
        image_config_digest: ` + testArchiveConfig + `
`
}
