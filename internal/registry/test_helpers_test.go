package registry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
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

type fakeContent struct {
	raw []byte
	err error
}

type fakeRepository struct {
	topDescriptor ocispec.Descriptor
	top           fakeContent
	contents      map[digest.Digest]fakeContent
	references    []string
	fetched       []ocispec.Descriptor
}

func (repository *fakeRepository) FetchReference(
	_ context.Context,
	reference string,
) (ocispec.Descriptor, io.ReadCloser, error) {
	repository.references = append(repository.references, reference)
	if repository.top.err != nil {
		return ocispec.Descriptor{}, nil, repository.top.err
	}

	return repository.topDescriptor, io.NopCloser(bytes.NewReader(repository.top.raw)), nil
}

func (repository *fakeRepository) Fetch(
	_ context.Context,
	descriptorValue ocispec.Descriptor,
) (io.ReadCloser, error) {
	repository.fetched = append(repository.fetched, descriptorValue)

	content, found := repository.contents[descriptorValue.Digest]
	if !found {
		return nil, errdef.ErrNotFound
	}

	if content.err != nil {
		return nil, content.err
	}

	return io.NopCloser(bytes.NewReader(content.raw)), nil
}

func sourceForTest(t *testing.T, value string) imageref.Source {
	t.Helper()

	source, err := imageref.Normalize(value)
	if err != nil {
		t.Fatalf("Normalize(%q) error = %v", value, err)
	}

	return source
}

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

	process, err := json.Marshal(imageProcessConfig{
		Entrypoint: []string{"/usr/local/bin/api"},
		Command:    []string{"serve"},
	})
	if err != nil {
		t.Fatalf("json.Marshal(image process config) error = %v", err)
	}

	raw, err := json.Marshal(imageConfig{
		Architecture: platform.Architecture,
		Config:       process,
		OS:           platform.OS,
		RootFS:       json.RawMessage(`{"type":"layers","diff_ids":[]}`),
		Variant:      platform.Variant,
	})
	if err != nil {
		t.Fatalf("json.Marshal(config) error = %v", err)
	}

	return raw, internalDescriptorForTest(raw, ocispec.MediaTypeImageConfig)
}

func manifestForTest(t *testing.T, config descriptor) ([]byte, ocispec.Descriptor) {
	t.Helper()

	raw, err := json.Marshal(imageManifestDocument{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageManifest,
		Config:        config,
		Layers:        []descriptor{},
	})
	if err != nil {
		t.Fatalf("json.Marshal(manifest) error = %v", err)
	}

	return raw, descriptorForTest(raw, ocispec.MediaTypeImageManifest)
}

func indexForTest(t *testing.T, manifests []descriptor) ([]byte, ocispec.Descriptor) {
	t.Helper()

	raw, err := json.Marshal(indexDocument{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageIndex,
		Manifests:     manifests,
	})
	if err != nil {
		t.Fatalf("json.Marshal(index) error = %v", err)
	}

	return raw, descriptorForTest(raw, ocispec.MediaTypeImageIndex)
}

func resolverForTest(repository remoteRepository) *Resolver {
	return newResolver(
		func(context.Context, registry.Reference, credential) (remoteRepository, error) {
			return repository, nil
		},
		nil,
	)
}
