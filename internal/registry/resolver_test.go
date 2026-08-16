package registry

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

func TestResolveSingleManifestSources(t *testing.T) {
	t.Parallel()

	platform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	configRaw, configDescriptor := configForTest(t, platform)
	manifestRaw, manifestDescriptor := manifestForTest(t, configDescriptor)

	tests := []struct {
		name      string
		source    string
		reference string
	}{
		{name: "implicit latest", source: testImageName, reference: "latest"},
		{name: "tag", source: "api:1", reference: "1"},
		{
			name:      "digest",
			source:    "api@" + manifestDescriptor.Digest.String(),
			reference: manifestDescriptor.Digest.String(),
		},
		{
			name:      "tag and digest",
			source:    "api:1@" + manifestDescriptor.Digest.String(),
			reference: manifestDescriptor.Digest.String(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assertSingleManifestSource(
				t,
				test.source,
				test.reference,
				platform,
				configRaw,
				configDescriptor,
				manifestRaw,
				manifestDescriptor,
			)
		})
	}
}

func assertSingleManifestSource(
	t *testing.T,
	sourceValue string,
	wantFetchedReference string,
	platform domain.Platform,
	configRaw []byte,
	configDescriptor descriptor,
	manifestRaw []byte,
	manifestDescriptor ocispec.Descriptor,
) {
	t.Helper()

	repository := &fakeRepository{
		topDescriptor: manifestDescriptor,
		top:           fakeContent{raw: manifestRaw},
		contents: map[digest.Digest]fakeContent{
			configDescriptor.Digest: {raw: configRaw},
		},
	}

	result, err := resolverForTest(repository).Resolve(
		context.Background(),
		sourceForTest(t, sourceValue),
		platform,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	wantReference := sourceForTest(t, sourceValue)

	pinned, err := wantReference.Pin(result.ReferenceDigest)
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}

	assertResolvedImage(t, result, pinned, platform, manifestDescriptor, configDescriptor)
	assertSingleManifestFetches(t, repository, wantFetchedReference, configDescriptor)
}

func assertResolvedImage(
	t *testing.T,
	result domain.ImageIdentity,
	wantReference imageref.Reference,
	wantPlatform domain.Platform,
	wantManifest ocispec.Descriptor,
	wantConfig descriptor,
) {
	t.Helper()

	if result.Reference != wantReference.String() || result.ReferenceDigest != wantReference.Digest() ||
		result.Platform != wantPlatform ||
		result.PlatformManifest.String() != wantManifest.Digest.String() ||
		result.ImageConfig.String() != wantConfig.Digest.String() {
		t.Fatalf("Resolve() = %#v", result)
	}
}

func assertSingleManifestFetches(
	t *testing.T,
	repository *fakeRepository,
	wantReference string,
	wantConfig descriptor,
) {
	t.Helper()

	if len(repository.references) != 1 || repository.references[0] != wantReference {
		t.Fatalf("FetchReference calls = %v", repository.references)
	}

	if len(repository.fetched) != 1 || repository.fetched[0].Digest != wantConfig.Digest {
		t.Fatalf("Fetch calls = %#v", repository.fetched)
	}
}

func TestResolveSelectsExactPlatformWithoutFetchingLayers(t *testing.T) {
	t.Parallel()

	target := domain.Platform{OS: testOSLinux, Architecture: testArchitectureARM64, Variant: "v8"}
	configRaw, configDescriptor := configForTest(t, target)
	manifestRaw, manifestDescriptor := manifestForTest(t, configDescriptor)
	selected := internalDescriptorForTest(manifestRaw, ocispec.MediaTypeImageManifest)
	selected.Platform = &imagePlatform{OS: testOSLinux, Architecture: testArchitectureARM64, Variant: "v8"}

	unknown := descriptor{
		MediaType: "application/vnd.example.attestation",
		Digest:    digest.Digest("invalid"),
		Size:      -1,
		Platform:  &imagePlatform{OS: testOSLinux, Architecture: testArchitectureARM64, Variant: "v8"},
	}
	indexRaw, indexDescriptor := indexForTest(t, []descriptor{unknown, selected})
	repository := &fakeRepository{
		topDescriptor: indexDescriptor,
		top:           fakeContent{raw: indexRaw},
		contents: map[digest.Digest]fakeContent{
			manifestDescriptor.Digest: {raw: manifestRaw},
			configDescriptor.Digest:   {raw: configRaw},
		},
	}

	result, err := resolverForTest(repository).Resolve(
		context.Background(),
		sourceForTest(t, "example.com/team/api:2"),
		target,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if result.Reference != "example.com/team/api:2@"+indexDescriptor.Digest.String() ||
		result.PlatformManifest.String() != manifestDescriptor.Digest.String() ||
		result.ImageConfig.String() != configDescriptor.Digest.String() {
		t.Fatalf("Resolve() = %#v", result)
	}

	if len(repository.fetched) != 2 || repository.fetched[0].Digest != manifestDescriptor.Digest ||
		repository.fetched[1].Digest != configDescriptor.Digest {
		t.Fatalf("Fetch calls = %#v", repository.fetched)
	}
}

func TestResolveRejectsUnavailableOrAmbiguousPlatform(t *testing.T) {
	t.Parallel()

	target := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	configRaw, configDescriptor := configForTest(t, target)
	manifestRaw, _ := manifestForTest(t, configDescriptor)
	selected := internalDescriptorForTest(manifestRaw, ocispec.MediaTypeImageManifest)
	selected.Platform = &imagePlatform{OS: testOSLinux, Architecture: testArchitectureARM64}

	tests := []struct {
		name      string
		manifests []descriptor
		want      error
	}{
		{name: "no match", manifests: []descriptor{selected}, want: ErrPlatformUnavailable},
		{name: "empty", manifests: []descriptor{}, want: ErrPlatformUnavailable},
		{
			name: "duplicate",
			manifests: []descriptor{
				withPlatform(selected, target),
				withPlatform(selected, target),
			},
			want: ErrProtocol,
		},
		{
			name: "invalid selected descriptor",
			manifests: []descriptor{
				withPlatform(descriptor{MediaType: ocispec.MediaTypeImageManifest}, target),
			},
			want: ErrProtocol,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			indexRaw, indexDescriptor := indexForTest(t, test.manifests)
			repository := &fakeRepository{
				topDescriptor: indexDescriptor,
				top:           fakeContent{raw: indexRaw},
				contents:      map[digest.Digest]fakeContent{configDescriptor.Digest: {raw: configRaw}},
			}

			_, err := resolverForTest(repository).Resolve(
				context.Background(),
				sourceForTest(t, testImageName),
				target,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestResolveChecksSelectedConfigPlatform(t *testing.T) {
	t.Parallel()

	target := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	wrongConfigRaw, wrongConfig := configForTest(
		t,
		domain.Platform{OS: testOSLinux, Architecture: testArchitectureARM64},
	)
	manifestRaw, manifestDescriptor := manifestForTest(t, wrongConfig)

	t.Run("single manifest", func(t *testing.T) {
		t.Parallel()

		repository := &fakeRepository{
			topDescriptor: manifestDescriptor,
			top:           fakeContent{raw: manifestRaw},
			contents:      map[digest.Digest]fakeContent{wrongConfig.Digest: {raw: wrongConfigRaw}},
		}

		_, err := resolverForTest(repository).Resolve(
			context.Background(),
			sourceForTest(t, testImageName),
			target,
		)
		if !errors.Is(err, ErrPlatformUnavailable) {
			t.Fatalf("Resolve() error = %v", err)
		}
	})

	t.Run("indexed manifest", func(t *testing.T) {
		t.Parallel()

		selected := internalDescriptorForTest(manifestRaw, ocispec.MediaTypeImageManifest)
		selected.Platform = &imagePlatform{OS: target.OS, Architecture: target.Architecture}
		indexRaw, indexDescriptor := indexForTest(t, []descriptor{selected})
		repository := &fakeRepository{
			topDescriptor: indexDescriptor,
			top:           fakeContent{raw: indexRaw},
			contents: map[digest.Digest]fakeContent{
				manifestDescriptor.Digest: {raw: manifestRaw},
				wrongConfig.Digest:        {raw: wrongConfigRaw},
			},
		}

		_, err := resolverForTest(repository).Resolve(
			context.Background(),
			sourceForTest(t, testImageName),
			target,
		)
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("Resolve() error = %v", err)
		}
	})
}

func TestResolveInputAndDependencyFailures(t *testing.T) {
	t.Parallel()

	validPlatform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	validSource := sourceForTest(t, testImageName)

	tests := []struct {
		name     string
		resolver *Resolver
		source   imageref.Source
		platform domain.Platform
		want     error
	}{
		{name: "nil resolver", resolver: nil, source: validSource, platform: validPlatform, want: ErrUnavailable},
		{name: "empty resolver", resolver: &Resolver{}, source: validSource, platform: validPlatform, want: ErrUnavailable},
		{name: "empty source", resolver: resolverForTest(&fakeRepository{}), platform: validPlatform, want: ErrInvalidSource},
		{
			name:     "invalid platform",
			resolver: resolverForTest(&fakeRepository{}),
			source:   validSource,
			platform: domain.Platform{OS: "Linux", Architecture: "amd64"},
			want:     ErrInvalidSource,
		},
		{
			name: "factory error",
			resolver: newResolver(
				func(context.Context, registry.Reference, credential) (remoteRepository, error) {
					return nil, errdef.ErrInvalidReference
				},
				nil,
			),
			source: validSource, platform: validPlatform, want: ErrInvalidSource,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := test.resolver.Resolve(context.Background(), test.source, test.platform)
			if !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestResolveCredentialProvider(t *testing.T) {
	t.Parallel()

	validPlatform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	validSource := sourceForTest(t, "example.com/team/api")
	wantCredential := Credentials{
		Username:     testUsername,
		Password:     testRegistrySecret,
		RefreshToken: testRefreshToken,
		AccessToken:  testAccessToken,
	}

	var gotRegistry string

	var gotCredential credential

	resolver := newResolver(
		func(_ context.Context, _ registry.Reference, value credential) (remoteRepository, error) {
			gotCredential = value

			return &fakeRepository{top: fakeContent{err: errdef.ErrNotFound}}, nil
		},
		func(_ context.Context, registry string) (Credentials, error) {
			gotRegistry = registry

			return wantCredential, nil
		},
	)

	_, err := resolver.Resolve(context.Background(), validSource, validPlatform)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Resolve() error = %v", err)
	}

	if gotRegistry != "example.com" || gotCredential.username != wantCredential.Username ||
		gotCredential.password != wantCredential.Password || gotCredential.refreshToken != wantCredential.RefreshToken ||
		gotCredential.accessToken != wantCredential.AccessToken {
		t.Fatalf("credential lookup = %q, %#v", gotRegistry, gotCredential)
	}
}

func TestResolveCredentialProviderFailure(t *testing.T) {
	t.Parallel()

	validPlatform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	validSource := sourceForTest(t, "example.com/team/api")
	failed := newResolver(
		func(context.Context, registry.Reference, credential) (remoteRepository, error) {
			t.Fatal("repository factory called after credential failure")

			return nil, ErrProtocol
		},
		func(context.Context, string) (Credentials, error) {
			return Credentials{}, fmt.Errorf("contains secret: %w", ErrProtocol)
		},
	)

	_, err := failed.Resolve(context.Background(), validSource, validPlatform)
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "secret") {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestResolveCredentialProviderCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	validPlatform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	validSource := sourceForTest(t, "example.com/team/api")
	cancelled := newResolver(
		func(context.Context, registry.Reference, credential) (remoteRepository, error) {
			t.Fatal("repository factory called after cancellation")

			return nil, ErrProtocol
		},
		func(context.Context, string) (Credentials, error) {
			return Credentials{}, context.Canceled
		},
	)

	_, err := cancelled.Resolve(ctx, validSource, validPlatform)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestCredentialsReturnsEphemeralProviderValue(t *testing.T) {
	t.Parallel()

	want := Credentials{
		Username:     testUsername,
		Password:     testPassword,
		RefreshToken: testRefreshToken,
		AccessToken:  testAccessToken,
	}
	source := sourceForTest(t, "example.com/team/api:1")

	reference, err := source.Pin(domain.Hash([]byte("credential reference")))
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}

	var gotRegistry string

	resolver := newResolver(nil, func(_ context.Context, registryName string) (Credentials, error) {
		gotRegistry = registryName

		return want, nil
	})

	got, err := resolver.Credentials(context.Background(), reference)
	if err != nil || got != want || gotRegistry != source.Registry() {
		t.Fatalf("Credentials() = %#v, %v", got, err)
	}
}

func TestCredentialsContainsInvalidProviderResults(t *testing.T) {
	t.Parallel()

	source := sourceForTest(t, "example.com/team/api:1")

	reference, err := source.Pin(domain.Hash([]byte("credential validation")))
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}

	tests := []struct {
		name     string
		resolver *Resolver
		want     error
	}{
		{name: "nil resolver", resolver: nil, want: ErrUnavailable},
		{
			name: "provider failure",
			resolver: newResolver(nil, func(context.Context, string) (Credentials, error) {
				return Credentials{}, ErrProtocol
			}),
			want: ErrUnavailable,
		},
		{
			name: "invalid UTF-8",
			resolver: newResolver(nil, func(context.Context, string) (Credentials, error) {
				return Credentials{
					Username: "", Password: string([]byte{0xff}), RefreshToken: "", AccessToken: "",
				}, nil
			}),
			want: ErrUnavailable,
		},
		{
			name: "oversized",
			resolver: newResolver(nil, func(context.Context, string) (Credentials, error) {
				return Credentials{
					Username: "", Password: strings.Repeat("x", maximumCredentialBytes+1),
					RefreshToken: "", AccessToken: "",
				}, nil
			}),
			want: ErrUnavailable,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := test.resolver.Credentials(context.Background(), reference)
			if !errors.Is(err, test.want) {
				t.Fatalf("Credentials() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCredentialsPreservesCancellationAndSupportsAnonymousAccess(t *testing.T) {
	t.Parallel()

	source := sourceForTest(t, "example.com/team/api:1")

	reference, err := source.Pin(domain.Hash([]byte("credential cancellation")))
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}

	empty, err := newResolver(nil, nil).Credentials(context.Background(), reference)
	if err != nil || empty != (Credentials{}) {
		t.Fatalf("Credentials(anonymous) = %#v, %v", empty, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cancelled := newResolver(nil, func(context.Context, string) (Credentials, error) {
		return Credentials{}, context.Canceled
	})

	_, err = cancelled.Credentials(ctx, reference)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("Credentials(cancelled) error = %v", err)
	}
}

func TestCredentialsRejectsEmptyReference(t *testing.T) {
	t.Parallel()

	_, err := newResolver(nil, nil).Credentials(context.Background(), imageref.Reference{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Credentials(empty reference) error = %v", err)
	}
}

func withPlatform(value descriptor, platform domain.Platform) descriptor {
	value.Platform = &imagePlatform{
		OS:           platform.OS,
		Architecture: platform.Architecture,
		Variant:      platform.Variant,
	}

	return value
}
