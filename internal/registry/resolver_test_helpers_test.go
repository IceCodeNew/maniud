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

	"github.com/IceCodeNew/maniud/internal/imageref"
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
