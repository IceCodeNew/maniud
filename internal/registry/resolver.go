package registry

import (
	"context"
	"io"
	"unicode/utf8"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	credentialvalue "github.com/IceCodeNew/maniud/internal/registry/credential"
)

// Repository supplies immutable OCI metadata to the shared image-graph
// verifier. Implementations may read a registry or a local content store.
type Repository interface {
	Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error)
	FetchReference(ctx context.Context, reference string) (ocispec.Descriptor, io.ReadCloser, error)
}

// LocalRepository additionally proves that every selected image layer exists
// with the descriptor's exact size and digest.
type LocalRepository interface {
	Repository
	Verify(ctx context.Context, target ocispec.Descriptor) error
}

// Credentials contains credentials for one registry.
type Credentials = credentialvalue.Value

// CredentialProvider returns explicit credentials for a normalized registry
// authority. Supplying one replaces Docker configuration lookup. Results must
// contain valid UTF-8 and no more than 16 KiB across all fields.
type CredentialProvider func(context.Context, string) (Credentials, error)

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
			credentialValue Credentials,
		) (Repository, error) {
			return newRepository(reference, credentialValue)
		},
	}
}

func newResolver(factory repositoryFactory, credentials CredentialProvider) *Resolver {
	return &Resolver{credentials: credentials, repositories: factory}
}

// Credentials returns the ephemeral credential for one canonical image
// reference. Callers must not persist or log the result.
func (resolver *Resolver) Credentials(
	ctx context.Context,
	reference imageref.Reference,
) (Credentials, error) {
	if resolver == nil || reference.Registry() == "" {
		return Credentials{}, ErrUnavailable
	}

	value, err := resolver.credential(ctx, reference.Registry())
	if err != nil {
		return Credentials{}, err
	}

	return value, nil
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

	return resolveRepository(ctx, source, target, parsed.ReferenceOrDefault(), repository, nil)
}

// ResolveLocal verifies an image selected from a local content store. In
// addition to the metadata graph, it streams every selected layer through the
// repository's descriptor verifier before returning an identity.
func ResolveLocal(
	ctx context.Context,
	source imageref.Source,
	platform domain.Platform,
	repository LocalRepository,
) (domain.ImageIdentity, error) {
	if repository == nil {
		return domain.ImageIdentity{}, ErrUnavailable
	}

	parsed, target, err := resolveInput(source, platform)
	if err != nil {
		return domain.ImageIdentity{}, ErrInvalidSource
	}

	return resolveRepository(ctx, source, target, parsed.ReferenceOrDefault(), repository, repository)
}

func resolveRepository(
	ctx context.Context,
	source imageref.Source,
	target imagePlatform,
	referenceName string,
	repository Repository,
	local LocalRepository,
) (domain.ImageIdentity, error) {
	top, err := fetchReferenceManifest(ctx, repository, referenceName)
	if err != nil {
		return domain.ImageIdentity{}, err
	}

	reference, err := source.Pin(top.digest)
	if err != nil {
		return domain.ImageIdentity{}, ErrProtocol
	}

	selected, err := manifestForPlatform(ctx, repository, top, target)
	if err != nil {
		return domain.ImageIdentity{}, err
	}
	if err := verifyLocalLayers(ctx, local, selected.layers); err != nil {
		return domain.ImageIdentity{}, err
	}

	return resolveImageConfig(ctx, repository, top, selected, target, reference)
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

func resolveImageConfig(
	ctx context.Context,
	repository Repository,
	top manifestDocument,
	selected manifestDocument,
	target imagePlatform,
	reference imageref.Reference,
) (domain.ImageIdentity, error) {
	config, configDigest, err := readConfig(ctx, repository, *selected.config)
	if err != nil {
		return domain.ImageIdentity{}, err
	}
	if len(config.configuration.DiffIDs) != len(selected.layers) {
		return domain.ImageIdentity{}, ErrProtocol
	}

	if !exactPlatform(&config.platform, target) {
		if top.config != nil {
			return domain.ImageIdentity{}, ErrPlatformUnavailable
		}

		return domain.ImageIdentity{}, ErrProtocol
	}

	return config.configuration.Identity(domain.ImageIdentity{
		Origin:          domain.ImageOriginRegistry,
		Reference:       reference.String(),
		ReferenceDigest: reference.Digest(),
		Platform: domain.Platform{
			OS:           target.OS,
			Architecture: target.Architecture,
			Variant:      target.Variant,
		},
		PlatformManifest: selected.digest,
		ImageConfig:      configDigest,
	}), nil
}

//nolint:ireturn // The repository factory is the transport seam used by the resolver and its protocol tests.
func (resolver *Resolver) openRepository(
	ctx context.Context,
	registryName string,
	reference registry.Reference,
) (Repository, error) {
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
	repository Repository,
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
	repository Repository,
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
	repository Repository,
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

func readConfig(
	ctx context.Context,
	repository Repository,
	descriptorValue descriptor,
) (imageConfigEvidence, domain.Digest, error) {
	if !validDescriptor(
		descriptorValue,
		maximumConfigBytes,
		dockerMediaTypeImageConfig,
		ocispec.MediaTypeImageConfig,
	) {
		return imageConfigEvidence{}, domain.Digest{}, ErrProtocol
	}

	configDescriptor := toOCIDescriptor(descriptorValue)

	reader, err := repository.Fetch(ctx, configDescriptor)
	if err != nil {
		return imageConfigEvidence{}, domain.Digest{}, classifyRemoteError(err)
	}

	raw, err := readVerified(reader, configDescriptor, maximumConfigBytes)
	if err != nil {
		return imageConfigEvidence{}, domain.Digest{}, classifyRemoteError(err)
	}

	return decodeImageConfig(raw, configDescriptor)
}

func (resolver *Resolver) credential(ctx context.Context, registry string) (Credentials, error) {
	if resolver.credentials == nil {
		var empty Credentials

		return empty, nil
	}

	value, err := resolver.credentials(ctx, registry)
	if err != nil {
		if ctx.Err() != nil {
			return Credentials{}, classifyRemoteError(ctx.Err())
		}

		return Credentials{}, ErrUnavailable
	}

	if !validCredentials(value) {
		return Credentials{}, ErrUnavailable
	}

	return value, nil
}

func validCredentials(value Credentials) bool {
	values := []string{value.Username, value.Password, value.RefreshToken, value.AccessToken}
	total := 0

	for _, item := range values {
		if !utf8.ValidString(item) {
			return false
		}

		total += len(item)
	}

	return total <= maximumCredentialBytes
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
