package registry

import (
	"encoding/json"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testArchitectureAMD64 = "amd64"
	testArchitectureARM64 = "arm64"
	testImageName         = "api"
	testInvalidMediaType  = "application/example"
	testOSLinux           = "linux"
	testPassword          = "password"
	testRefreshToken      = "refresh"
	testAccessToken       = "access"
	testInvalidDescriptor = "invalid descriptor"
	testRegistrySecret    = "secret"
	testRegistryUsername  = "robot"
	testUsername          = "user"
)

func descriptorForTest(raw []byte, mediaType string) ocispec.Descriptor {
	return ocispec.Descriptor{
		MediaType: mediaType,
		Digest:    digest.Digest(domain.Hash(raw).String()),
		Size:      int64(len(raw)),
	}
}

func internalDescriptorForTest(raw []byte, mediaType string) descriptor {
	descriptorValue := descriptorForTest(raw, mediaType)

	return descriptor{
		MediaType: descriptorValue.MediaType,
		Digest:    descriptorValue.Digest,
		Size:      descriptorValue.Size,
	}
}

//nolint:tagliatelle // OCI defines these wire-field names.
type testImageProcessConfig struct {
	Entrypoint []string `json:"Entrypoint,omitempty"`
	Command    []string `json:"Cmd,omitempty"`
}

type testImageConfig struct {
	Architecture string          `json:"architecture"`
	Config       json.RawMessage `json:"config,omitempty"`
	OS           string          `json:"os"`
	RootFS       json.RawMessage `json:"rootfs"`
	Variant      string          `json:"variant,omitempty"`
}

func configForTest(t *testing.T, platform domain.Platform, diffIDs ...domain.Digest) ([]byte, descriptor) {
	t.Helper()

	process, err := json.Marshal(testImageProcessConfig{
		Entrypoint: []string{"/usr/local/bin/api"},
		Command:    []string{"serve"},
	})
	if err != nil {
		t.Fatalf("json.Marshal(image process config) error = %v", err)
	}

	encodedDiffIDs := make([]string, len(diffIDs))
	for index, value := range diffIDs {
		encodedDiffIDs[index] = value.String()
	}
	rootFS, err := json.Marshal(struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	}{Type: "layers", DiffIDs: encodedDiffIDs})
	if err != nil {
		t.Fatalf("json.Marshal(rootfs) error = %v", err)
	}

	raw, err := json.Marshal(testImageConfig{
		Architecture: platform.Architecture,
		Config:       process,
		OS:           platform.OS,
		RootFS:       rootFS,
		Variant:      platform.Variant,
	})
	if err != nil {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}

	return raw, internalDescriptorForTest(raw, ocispec.MediaTypeImageConfig)
}
