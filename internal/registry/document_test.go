package registry

import (
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestDecodeManifestAcceptsDockerAndOCIImages(t *testing.T) {
	t.Parallel()

	configRaw, configDescriptor := configForTest(
		t,
		domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64},
	)
	_ = configRaw

	for _, mediaType := range []string{dockerMediaTypeManifest, ocispec.MediaTypeImageManifest} {
		raw := []byte(`{"schemaVersion":2,"config":{"mediaType":"` + configDescriptor.MediaType +
			`","digest":"` + configDescriptor.Digest.String() + `","size":` +
			stringNumber(configDescriptor.Size) + `},"layers":[],"annotations":{"key":"value"}}`)
		descriptorValue := descriptorForTest(raw, mediaType)

		document, err := decodeManifest(raw, descriptorValue)
		if err != nil || document.config == nil || document.digest.String() != descriptorValue.Digest.String() {
			t.Fatalf("decodeManifest(%q) = %#v, %v", mediaType, document, err)
		}
	}
}

func TestDecodeManifestRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		mediaType string
	}{
		{
			name:      "duplicate key",
			raw:       `{"schemaVersion":2,"schemaVersion":2}`,
			mediaType: ocispec.MediaTypeImageIndex,
		},
		{
			name:      "unknown field",
			raw:       `{"schemaVersion":2,"manifests":[],"unknown":true}`,
			mediaType: ocispec.MediaTypeImageIndex,
		},
		{
			name:      "wrong schema",
			raw:       `{"schemaVersion":1,"manifests":[]}`,
			mediaType: ocispec.MediaTypeImageIndex,
		},
		{
			name:      "wrong body media type",
			raw:       `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","manifests":[]}`,
			mediaType: ocispec.MediaTypeImageIndex,
		},
		{
			name:      "artifact index",
			raw:       `{"schemaVersion":2,"artifactType":"application/example","manifests":[]}`,
			mediaType: ocispec.MediaTypeImageIndex,
		},
		{
			name:      "subject manifest",
			raw:       `{"schemaVersion":2,"config":{},"layers":[],"subject":{}}`,
			mediaType: ocispec.MediaTypeImageManifest,
		},
		{
			name:      "unsupported",
			raw:       `{}`,
			mediaType: "application/example",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw := []byte(test.raw)

			_, err := decodeManifest(raw, descriptorForTest(raw, test.mediaType))
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("decodeManifest() error = %v", err)
			}
		})
	}
}

//nolint:funlen // The table is the strict image-config rejection corpus.
func TestDecodeImageConfigRejectsInvalidContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		raw       string
		mediaType string
	}{
		{name: "malformed", raw: `{`, mediaType: ocispec.MediaTypeImageConfig},
		{
			name:      "unknown field",
			raw:       `{"architecture":"amd64","os":"linux","rootfs":{},"unknown":true}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:      "unsupported media type",
			raw:       `{"architecture":"amd64","os":"linux","rootfs":{}}`,
			mediaType: "application/example",
		},
		{
			name:      "invalid platform",
			raw:       `{"architecture":"AMD64","os":"linux","rootfs":{}}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:      "os version",
			raw:       `{"architecture":"amd64","os":"linux","os.version":"1","rootfs":{}}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:      "os features",
			raw:       `{"architecture":"amd64","os":"linux","os.features":["x"],"rootfs":{}}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:      "process config shape",
			raw:       `{"architecture":"amd64","os":"linux","config":[],"rootfs":{}}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:      "process arguments",
			raw:       `{"architecture":"amd64","os":"linux","config":{"Entrypoint":1},"rootfs":{}}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:      "unknown process field",
			raw:       `{"architecture":"amd64","os":"linux","config":{"Unknown":true},"rootfs":{}}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:      "duplicate process field",
			raw:       `{"architecture":"amd64","os":"linux","config":{"Cmd":[],"Cmd":[]},"rootfs":{}}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name:      "NUL process argument",
			raw:       `{"architecture":"amd64","os":"linux","config":{"Cmd":["bad\u0000value"]},"rootfs":{}}`,
			mediaType: ocispec.MediaTypeImageConfig,
		},
		{
			name: "invalid UTF-8 process argument",
			raw: "{\"architecture\":\"amd64\",\"os\":\"linux\",\"config\":{\"Cmd\":[\"" +
				string([]byte{0xff}) + "\"]},\"rootfs\":{}}",
			mediaType: ocispec.MediaTypeImageConfig,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw := []byte(test.raw)

			_, _, err := decodeImageConfig(raw, descriptorForTest(raw, test.mediaType))
			if !errors.Is(err, ErrProtocol) {
				t.Fatalf("decodeImageConfig() error = %v", err)
			}
		})
	}
}

func TestDecodeImageConfigPreservesProcessDefaults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		config         string
		wantEntrypoint []string
		wantCommand    []string
	}{
		{name: "omitted", config: "", wantEntrypoint: nil, wantCommand: nil},
		{name: "null", config: `,"config":null`, wantEntrypoint: nil, wantCommand: nil},
		{
			name: "explicit", config: `,"config":{"Entrypoint":["/bin/api"],"Cmd":[]}`,
			wantEntrypoint: []string{"/bin/api"}, wantCommand: []string{},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			raw := []byte(`{"architecture":"amd64","os":"linux","rootfs":{}` + test.config + `}`)

			config, digest, err := decodeImageConfig(raw, descriptorForTest(raw, ocispec.MediaTypeImageConfig))
			if err != nil || digest == (domain.Digest{}) ||
				(config.entrypoint == nil) != (test.wantEntrypoint == nil) ||
				(config.command == nil) != (test.wantCommand == nil) ||
				!slices.Equal(config.entrypoint, test.wantEntrypoint) ||
				!slices.Equal(config.command, test.wantCommand) {
				t.Fatalf("decodeImageConfig(%s) = %#v, %s, %v", test.name, config, digest, err)
			}
		})
	}
}

func TestDescriptorValidation(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"value":1}`)
	valid := descriptorForTest(raw, ocispec.MediaTypeImageManifest)

	_, err := decodeManifest([]byte(`{}`), valid)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeManifest() error = %v", err)
	}

	invalidRaw := valid

	invalidRaw.Digest = digest.Digest("sha256:" + strings.Repeat("f", 64))
	if _, validDescriptor := validRawDescriptor(invalidRaw, raw, int64(len(raw))); validDescriptor {
		t.Fatal("validRawDescriptor() accepted mismatched digest")
	}

	internal := internalDescriptorForTest(raw, ocispec.MediaTypeImageManifest)

	internal.URLs = []string{"https://example.invalid/content"}
	if validDescriptor(internal, int64(len(raw)), ocispec.MediaTypeImageManifest) {
		t.Fatal("validDescriptor() accepted external URL")
	}
}

func stringNumber(value int64) string {
	return strconv.FormatInt(value, 10)
}
