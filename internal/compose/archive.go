package compose

import (
	"reflect"
	"strings"

	"github.com/IceCodeNew/maniud/internal/composeext/maniud"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

const (
	archiveLinuxOS      = "linux"
	archiveAMD64        = "amd64"
	archiveARM64        = "arm64"
	archiveARM64Variant = "v8"
)

type archiveSource struct {
	archiveDigest   domain.Digest
	archiveSize     int64
	manifestDigest  domain.Digest
	memberIndex     int
	platform        domain.Platform
	selector        string
	sourceReference string
	identity        domain.ImageIdentity
}

// ImageInputKind identifies the proof path for one desired image.
type ImageInputKind uint8

const (
	// ImageInputRegistry resolves a registry source before projection.
	ImageInputRegistry ImageInputKind = iota + 1
	// ImageInputDockerArchive carries identity emitted by strict archive analysis.
	ImageInputDockerArchive
)

// ImageInput is one validated registry request or Docker archive identity.
type ImageInput struct {
	kind     ImageInputKind
	registry imageref.Source
	archive  domain.ImageIdentity
}

// Kind returns the selected image proof path.
func (input ImageInput) Kind() ImageInputKind {
	return input.kind
}

// RegistrySource returns the normalized registry source when Kind is registry.
func (input ImageInput) RegistrySource() (imageref.Source, bool) {
	return input.registry, input.kind == ImageInputRegistry
}

// ArchiveIdentity returns the embedded archive identity when Kind is Docker archive.
func (input ImageInput) ArchiveIdentity() (domain.ImageIdentity, bool) {
	return input.archive, input.kind == ImageInputDockerArchive
}

// ImageInput returns the validated image proof request for one active service.
func (project Project) ImageInput(serviceName string) (ImageInput, error) {
	selected, err := project.service(serviceName)
	if err != nil {
		return ImageInput{}, err
	}
	if archive, found := project.extension.archives[selected.Name]; found {
		identity, _ := archiveIdentity(selected.Image, selected.Platform, selected.PullPolicy, archive)

		return ImageInput{kind: ImageInputDockerArchive, archive: identity}, nil
	}

	source, err := imageref.Normalize(selected.Image)
	if err != nil {
		return ImageInput{}, ErrInvalidSource
	}

	return ImageInput{kind: ImageInputRegistry, registry: source}, nil
}

func archiveSourceFromProof(proof maniud.ArchiveProof) (archiveSource, bool) {
	archiveDigest, archiveValid := domain.ParseDigest(proof.ArchiveDigest.String())
	manifestDigest, manifestValid := domain.ParseDigest(proof.ManifestDigest.String())
	referenceDigest, referenceValid := domain.ParseDigest(proof.ReferenceDigest.String())
	platformManifest, platformValid := domain.ParseDigest(proof.PlatformManifestDigest.String())
	imageConfig, imageConfigValid := domain.ParseDigest(proof.ImageConfigDigest.String())
	if archiveValid != nil || manifestValid != nil || referenceValid != nil || platformValid != nil ||
		imageConfigValid != nil {
		return archiveSource{}, false
	}

	return archiveSource{
		archiveDigest: archiveDigest, archiveSize: proof.ArchiveSize,
		manifestDigest: manifestDigest, memberIndex: proof.MemberIndex,
		platform: proof.Platform, selector: proof.Selector, sourceReference: proof.SourceReference,
		identity: domain.ImageIdentity{
			Origin: domain.ImageOriginDockerArchive, ReferenceDigest: referenceDigest,
			Platform: proof.Platform, PlatformManifest: platformManifest, ImageConfig: imageConfig,
		},
	}, true
}

func validArchiveService(image, platform, pullPolicy string, source archiveSource) bool {
	_, valid := archiveIdentity(image, platform, pullPolicy, source)

	return valid
}

func archiveIdentity(image, platform, pullPolicy string, source archiveSource) (domain.ImageIdentity, bool) {
	if pullPolicy != "never" || !archiveReferenceMatches(image, source) {
		return domain.ImageIdentity{}, false
	}
	parsedPlatform, valid := archivePlatform(platform)
	if !valid || parsedPlatform != source.platform {
		return domain.ImageIdentity{}, false
	}

	identity := source.identity
	identity.Reference = image
	identity.Platform = parsedPlatform

	return identity, true
}

func archiveReferenceMatches(image string, source archiveSource) bool {
	if source.sourceReference != "" {
		return image == source.sourceReference
	}

	want := "localhost/maniud/archive:source-" + strings.TrimPrefix(source.manifestDigest.String(), "sha256:")

	return image == want
}

func archivePlatform(value string) (domain.Platform, bool) {
	switch value {
	case "linux/amd64":
		return domain.Platform{OS: archiveLinuxOS, Architecture: archiveAMD64, Variant: ""}, true
	case "linux/arm64/v8":
		return domain.Platform{
			OS: archiveLinuxOS, Architecture: archiveARM64, Variant: archiveARM64Variant,
		}, true
	default:
		return domain.Platform{}, false
	}
}

func sameArchiveIdentity(left, right domain.ImageIdentity) bool {
	return reflect.DeepEqual(left, right)
}
