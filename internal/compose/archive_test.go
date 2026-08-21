package compose

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/IceCodeNew/maniud/internal/composeext/maniud"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testArchiveDigest  = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testManifestDigest = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testArchiveConfig  = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testArchiveImage   = "example.com/team/archive:1"
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

func TestArchiveProofConversionRejectsUnsupportedDigest(t *testing.T) {
	t.Parallel()

	if _, valid := archiveSourceFromProof(maniud.ArchiveProof{
		ArchiveDigest: digest.Digest("sha512:" + strings.Repeat("a", 128)),
	}); valid {
		t.Fatal("archiveSourceFromProof accepted a non-SHA-256 digest")
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
