package registry

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestDecodeManifestAcceptsDockerAndOCIImages(t *testing.T) {
	t.Parallel()

	configRaw, configDescriptor := configForTest(
		t,
		Platform{OS: testOSLinux, Architecture: testArchitectureAMD64},
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
