package registry

import (
	"context"
	"io"
	"slices"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

// Options configures a registry resolver.
type Options struct {
	Credentials      CredentialProvider
	DockerConfigPath string
}

// Resolver reads and verifies image metadata without pulling image layers.
type Resolver struct {
	credentials  CredentialProvider
	repositories repositoryFactory
}

// NewResolver creates a TLS registry resolver. Unless options supplies an
// explicit provider, the resolver reads static credentials from Docker config.
func NewResolver(options Options) *Resolver {
	credentialProvider := options.Credentials
	if credentialProvider == nil {
		credentialProvider = dockerCredentialProvider(options.DockerConfigPath)
	}

	return &Resolver{
		credentials: credentialProvider,
		repositories: func(
			_ context.Context,
			reference registry.Reference,
			credentialValue credential,
		) (remoteRepository, error) {
			return newRepository(reference, credentialValue)
		},
	}
}

func newResolver(factory repositoryFactory, credentials CredentialProvider) *Resolver {
	return &Resolver{credentials: credentials, repositories: factory}
}

// Resolve resolves source for platform and verifies its manifest and image config.
func (resolver *Resolver) Resolve(
	ctx context.Context,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	var empty domain.ImageIdentity

	if resolver == nil || resolver.repositories == nil {
		return empty, ErrUnavailable
	}

	parsed, target, err := resolveInput(source, platform)
	if err != nil {
		return empty, ErrInvalidSource
	}

	repository, err := resolver.openRepository(ctx, source.Registry(), parsed)
	if err != nil {
		return empty, err
	}

	top, err := fetchReferenceManifest(ctx, repository, parsed.ReferenceOrDefault())
	if err != nil {
		return empty, err
	}

	reference, err := source.Pin(top.digest)
	if err != nil {
		return empty, ErrProtocol
	}

	selected, err := manifestForPlatform(ctx, repository, top, target)
	if err != nil {
		return empty, err
	}

	return resolver.resolveImageConfig(ctx, repository, top, selected, target, reference)
}

func resolveInput(source imageref.Source, platform domain.Platform) (registry.Reference, imagePlatform, error) {
	parsed, err := registry.ParseReference(source.String())
	if err != nil {
		return registry.Reference{}, imagePlatform{}, ErrInvalidSource
	}

	target, err := normalizePlatform(platform)
	if err != nil {
		return registry.Reference{}, imagePlatform{}, ErrInvalidSource
	}

	return parsed, target, nil
}

func (resolver *Resolver) resolveImageConfig(
	ctx context.Context,
	repository remoteRepository,
	top manifestDocument,
	selected manifestDocument,
	target imagePlatform,
	reference imageref.Reference,
) (domain.ImageIdentity, error) {
	configPlatform, configDigest, err := resolver.readConfig(ctx, repository, *selected.config)
	if err != nil {
		return domain.ImageIdentity{}, err
	}

	if !exactPlatform(&configPlatform, target) {
		if top.config != nil {
			return domain.ImageIdentity{}, ErrPlatformUnavailable
		}

		return domain.ImageIdentity{}, ErrProtocol
	}

	return domain.ImageIdentity{
		Reference:       reference.String(),
		ReferenceDigest: reference.Digest(),
		Platform: domain.Platform{
			OS:           target.OS,
			Architecture: target.Architecture,
			Variant:      target.Variant,
		},
		PlatformManifest: selected.digest,
		ImageConfig:      configDigest,
	}, nil
}

//nolint:ireturn // The repository factory is the transport seam used by the resolver and its protocol tests.
func (resolver *Resolver) openRepository(
	ctx context.Context,
	registryName string,
	reference registry.Reference,
) (remoteRepository, error) {
	credentialValue, err := resolver.credential(ctx, registryName)
	if err != nil {
		return nil, err
	}

	repository, err := resolver.repositories(ctx, reference, credentialValue)
	if err != nil {
		return nil, ErrInvalidSource
	}

	return repository, nil
}

func fetchReferenceManifest(
	ctx context.Context,
	repository remoteRepository,
	reference string,
) (manifestDocument, error) {
	descriptorValue, reader, err := repository.FetchReference(ctx, reference)
	if err != nil {
		return manifestDocument{}, classifyRemoteError(err)
	}

	return decodeFetchedManifest(reader, descriptorValue)
}

func fetchManifest(
	ctx context.Context,
	repository remoteRepository,
	descriptorValue ocispec.Descriptor,
) (manifestDocument, error) {
	reader, err := repository.Fetch(ctx, descriptorValue)
	if err != nil {
		return manifestDocument{}, classifyRemoteError(err)
	}

	return decodeFetchedManifest(reader, descriptorValue)
}

func decodeFetchedManifest(reader io.ReadCloser, descriptorValue ocispec.Descriptor) (manifestDocument, error) {
	raw, err := readVerified(reader, descriptorValue, maximumManifestBytes)
	if err != nil {
		return manifestDocument{}, classifyRemoteError(err)
	}

	document, err := decodeManifest(raw, descriptorValue)
	if err != nil {
		return manifestDocument{}, ErrProtocol
	}

	return document, nil
}

func manifestForPlatform(
	ctx context.Context,
	repository remoteRepository,
	top manifestDocument,
	target imagePlatform,
) (manifestDocument, error) {
	if top.config != nil {
		return top, nil
	}

	selected, err := selectPlatform(top.manifests, target)
	if err != nil {
		return manifestDocument{}, err
	}

	return fetchManifest(ctx, repository, toOCIDescriptor(selected))
}

func exactPlatform(value *imagePlatform, target imagePlatform) bool {
	return value != nil && value.OS == target.OS && value.Architecture == target.Architecture &&
		value.OSVersion == target.OSVersion && value.Variant == target.Variant &&
		slices.Equal(value.OSFeatures, target.OSFeatures) && slices.Equal(value.Features, target.Features)
}

func selectPlatform(values []descriptor, target imagePlatform) (descriptor, error) {
	var selected descriptor

	found := false

	for _, value := range values {
		if !slices.Contains(
			[]string{dockerMediaTypeManifest, ocispec.MediaTypeImageManifest},
			value.MediaType,
		) {
			continue
		}

		if !validDescriptor(
			value,
			maximumManifestBytes,
			dockerMediaTypeManifest,
			ocispec.MediaTypeImageManifest,
		) {
			return descriptor{}, ErrProtocol
		}

		if !exactPlatform(value.Platform, target) {
			continue
		}

		if found {
			return descriptor{}, ErrProtocol
		}

		selected = value
		found = true
	}

	if !found {
		return descriptor{}, ErrPlatformUnavailable
	}

	return selected, nil
}

func (resolver *Resolver) readConfig(
	ctx context.Context,
	repository remoteRepository,
	descriptorValue descriptor,
) (imagePlatform, domain.Digest, error) {
	if !validDescriptor(
		descriptorValue,
		maximumConfigBytes,
		dockerMediaTypeImageConfig,
		ocispec.MediaTypeImageConfig,
	) {
		return imagePlatform{}, domain.Digest{}, ErrProtocol
	}

	configDescriptor := toOCIDescriptor(descriptorValue)

	reader, err := repository.Fetch(ctx, configDescriptor)
	if err != nil {
		return imagePlatform{}, domain.Digest{}, classifyRemoteError(err)
	}

	raw, err := readVerified(reader, configDescriptor, maximumConfigBytes)
	if err != nil {
		return imagePlatform{}, domain.Digest{}, classifyRemoteError(err)
	}

	return decodeImageConfig(raw, configDescriptor)
}

func (resolver *Resolver) credential(ctx context.Context, registry string) (credential, error) {
	if resolver.credentials == nil {
		var empty credential

		return empty, nil
	}

	value, err := resolver.credentials(ctx, registry)
	if err != nil {
		if ctx.Err() != nil {
			return credential{}, classifyRemoteError(ctx.Err())
		}

		return credential{}, ErrUnavailable
	}

	return credential{
		username:     value.Username,
		password:     value.Password,
		refreshToken: value.RefreshToken,
		accessToken:  value.AccessToken,
	}, nil
}

func toOCIDescriptor(value descriptor) ocispec.Descriptor {
	return ocispec.Descriptor{
		MediaType:    value.MediaType,
		Digest:       value.Digest,
		Size:         value.Size,
		URLs:         value.URLs,
		Annotations:  value.Annotations,
		Data:         value.Data,
		Platform:     nil,
		ArtifactType: value.ArtifactType,
	}
}
