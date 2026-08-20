package registry

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
	orasregistry "oras.land/oras-go/v2/registry"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestNewResolver(t *testing.T) {
	t.Parallel()

	provider := func(context.Context, string) (Credentials, error) {
		return Credentials{}, nil
	}

	resolver := NewResolver(Options{Credentials: provider})
	if resolver == nil || resolver.credentials == nil || resolver.repositories == nil {
		t.Fatalf("NewResolver() = %#v", resolver)
	}

	defaultResolver := NewResolver(Options{DockerConfigPath: filepath.Join(t.TempDir(), "missing")})
	credentials, err := defaultResolver.credentials(context.Background(), "registry.example")
	if err != nil || credentials != (Credentials{}) {
		t.Fatalf("NewResolver(default credentials) = %#v, %v", credentials, err)
	}

	reference, err := orasregistry.ParseReference("example.com/team/api:1")
	if err != nil {
		t.Fatalf("ParseReference() error = %v", err)
	}

	repository, err := resolver.repositories(context.Background(), reference, Credentials{})
	if err != nil || repository == nil {
		t.Fatalf("default repository factory = %T, %v", repository, err)
	}
}

func TestResolveRejectsInvalidTopLevelContent(t *testing.T) {
	t.Parallel()

	platform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	configRaw, configDescriptor := configForTest(t, platform)
	manifestRaw, manifestDescriptor := manifestForTest(t, configDescriptor)

	tests := []struct {
		name       string
		source     string
		descriptor ocispec.Descriptor
		raw        []byte
		want       error
	}{
		{
			name:       testInvalidDescriptor,
			source:     testImageName,
			descriptor: ocispec.Descriptor{Size: -1},
			raw:        manifestRaw,
			want:       ErrProtocol,
		},
		{
			name:       "invalid manifest",
			source:     testImageName,
			descriptor: descriptorForTest([]byte(`{}`), ocispec.MediaTypeImageManifest),
			raw:        []byte(`{}`),
			want:       ErrProtocol,
		},
		{
			name:       "pinned digest conflict",
			source:     "api@sha256:ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
			descriptor: manifestDescriptor,
			raw:        manifestRaw,
			want:       ErrProtocol,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			repository := &fakeRepository{
				topDescriptor: test.descriptor,
				top:           fakeContent{raw: test.raw},
				contents:      map[digest.Digest]fakeContent{configDescriptor.Digest: {raw: configRaw}},
			}

			_, err := resolverForTest(repository).Resolve(
				context.Background(),
				sourceForTest(t, test.source),
				platform,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestResolveRejectsInvalidSelectedManifest(t *testing.T) {
	t.Parallel()

	platform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	configRaw, configDescriptor := configForTest(t, platform)
	validManifestRaw, _ := manifestForTest(t, configDescriptor)

	tests := []struct {
		name            string
		manifestRaw     []byte
		manifestContent fakeContent
		want            error
	}{
		{
			name:            "fetch failure",
			manifestRaw:     validManifestRaw,
			manifestContent: fakeContent{err: errdef.ErrNotFound},
			want:            ErrNotFound,
		},
		{
			name:            "read mismatch",
			manifestRaw:     validManifestRaw,
			manifestContent: fakeContent{raw: []byte(`{}`)},
			want:            ErrProtocol,
		},
		{
			name:            "invalid document",
			manifestRaw:     []byte(`{}`),
			manifestContent: fakeContent{raw: []byte(`{}`)},
			want:            ErrProtocol,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			selected := internalDescriptorForTest(test.manifestRaw, ocispec.MediaTypeImageManifest)
			selected.Platform = &imagePlatform{OS: platform.OS, Architecture: platform.Architecture}
			indexRaw, indexDescriptor := indexForTest(t, []descriptor{selected})
			repository := &fakeRepository{
				topDescriptor: indexDescriptor,
				top:           fakeContent{raw: indexRaw},
				contents: map[digest.Digest]fakeContent{
					selected.Digest:         test.manifestContent,
					configDescriptor.Digest: {raw: configRaw},
				},
			}

			_, err := resolverForTest(repository).Resolve(
				context.Background(),
				sourceForTest(t, testImageName),
				platform,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestResolveRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	platform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	validConfigRaw, validConfig := configForTest(t, platform)

	tests := []struct {
		name          string
		config        descriptor
		configContent fakeContent
		want          error
	}{
		{
			name:          testInvalidDescriptor,
			config:        descriptor{MediaType: ocispec.MediaTypeImageConfig},
			configContent: fakeContent{raw: validConfigRaw},
			want:          ErrProtocol,
		},
		{
			name:          "fetch failure",
			config:        validConfig,
			configContent: fakeContent{err: errdef.ErrNotFound},
			want:          ErrNotFound,
		},
		{
			name:          "read mismatch",
			config:        validConfig,
			configContent: fakeContent{raw: []byte(`{}`)},
			want:          ErrProtocol,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			manifestRaw, manifestDescriptor := manifestForTest(t, test.config)
			repository := &fakeRepository{
				topDescriptor: manifestDescriptor,
				top:           fakeContent{raw: manifestRaw},
				contents:      map[digest.Digest]fakeContent{test.config.Digest: test.configContent},
			}

			_, err := resolverForTest(repository).Resolve(
				context.Background(),
				sourceForTest(t, testImageName),
				platform,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, test.want)
			}
		})
	}
}
