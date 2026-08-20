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
	testOSLinux           = "linux"
	testPassword          = "password"
	testRefreshToken      = "refresh"
	testAccessToken       = "access"
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

func configForTest(t *testing.T, platform domain.Platform) ([]byte, descriptor) {
	t.Helper()

	raw, err := json.Marshal(imageConfig{
		Architecture: platform.Architecture,
		OS:           platform.OS,
		RootFS:       json.RawMessage(`{"type":"layers","diff_ids":[]}`),
		Variant:      platform.Variant,
	})
	if err != nil {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}

	return raw, internalDescriptorForTest(raw, ocispec.MediaTypeImageConfig)
}
