package compose

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

const (
	archiveExtensionKey       = "x-maniud"
	archiveKind               = "docker-archive"
	maximumArchiveSize        = int64(1 << 40)
	maximumArchiveMember      = 1_000_000
	archiveMetadataFieldCount = 10
	archiveLinuxOS            = "linux"
	archiveAMD64              = "amd64"
	archiveARM64              = "arm64"
	archiveARM64Variant       = "v8"
	archivePlatformField      = "platform"
	archiveImageSourceKey     = "image_source"
	runtimeMetadataField      = "runtime"
	composeDockerRuntime      = "docker"
	composePodmanRuntime      = "podman"
	composeNerdctlRuntime     = "nerdctl"
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

type archiveMetadata struct {
	archiveDigest    domain.Digest
	manifestDigest   domain.Digest
	referenceDigest  domain.Digest
	platformManifest domain.Digest
	imageConfig      domain.Digest
	archiveSize      int64
	memberIndex      int
	platform         domain.Platform
	selector         string
	sourceReference  string
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
	if archive, found := project.archives[selected.Name]; found {
		identity, _ := archiveIdentity(selected.Image, selected.Platform, selected.PullPolicy, archive)

		return ImageInput{kind: ImageInputDockerArchive, archive: identity}, nil
	}

	source, err := imageref.Normalize(selected.Image)
	if err != nil {
		return ImageInput{}, ErrInvalidSource
	}

	return ImageInput{kind: ImageInputRegistry, registry: source}, nil
}

func decodeManiudSources(
	document map[string]any,
) (map[string]archiveSource, map[string]domain.RuntimeKind, bool, bool) {
	raw, found := document[archiveExtensionKey]
	if !found {
		return nil, nil, false, true
	}
	extension, valid := exactMapping(raw, "services")
	if !valid {
		return nil, nil, true, false
	}
	rawServices, valid := extension["services"].(map[string]any)
	if !valid || len(rawServices) == 0 {
		return nil, nil, true, false
	}

	archives := make(map[string]archiveSource, len(rawServices))
	runtimes := make(map[string]domain.RuntimeKind, len(rawServices))
	for serviceName, rawService := range rawServices {
		service, archive, runtimeKind, serviceValid := decodeManiudService(rawService)
		if !serviceValid || serviceName == "" {
			return nil, nil, true, false
		}
		runtimes[serviceName] = runtimeKind
		if archive {
			archives[serviceName] = service
		}
	}

	return archives, runtimes, true, true
}

//nolint:cyclop // Runtime provenance and archive metadata are mutually exclusive service variants.
func decodeManiudService(raw any) (archiveSource, bool, domain.RuntimeKind, bool) {
	service, valid := raw.(map[string]any)
	if !valid || len(service) == 0 || len(service) > 2 {
		return archiveSource{}, false, "", false
	}
	runtimeKind := domain.RuntimeDocker
	if runtimeName, found := service[runtimeMetadataField]; found {
		var runtimeValid bool
		runtimeKind, runtimeValid = runtimeProvenance(runtimeName)
		if !runtimeValid {
			return archiveSource{}, false, "", false
		}
	}
	rawImage, archive := service[archiveImageSourceKey]
	if !archive {
		_, runtimeFound := service[runtimeMetadataField]

		return archiveSource{}, false, runtimeKind, runtimeFound
	}
	imageSource, valid := rawImage.(map[string]any)
	if !valid {
		return archiveSource{}, false, "", false
	}
	for key := range service {
		if key != runtimeMetadataField && key != archiveImageSourceKey {
			return archiveSource{}, false, "", false
		}
	}
	decoded, valid := decodeArchiveImageSource(imageSource)

	return decoded, true, runtimeKind, valid
}

func runtimeProvenance(raw any) (domain.RuntimeKind, bool) {
	value, valid := raw.(string)
	if !valid {
		return "", false
	}

	switch value {
	case composeDockerRuntime:
		return domain.RuntimeDocker, true
	case composePodmanRuntime:
		return domain.RuntimePodman, true
	case composeNerdctlRuntime:
		return domain.RuntimeContainerd, true
	default:
		return "", false
	}
}

func decodeArchiveImageSource(raw map[string]any) (archiveSource, bool) {
	if !archiveMetadataFieldsValid(raw) || stringValue(raw["kind"]) != archiveKind {
		return archiveSource{}, false
	}
	metadata, valid := decodeArchiveMetadata(raw)
	if !valid || metadata.manifestDigest != metadata.referenceDigest ||
		!validArchiveSelector(metadata.selector, metadata.memberIndex, metadata.sourceReference) {
		return archiveSource{}, false
	}

	return archiveSource{
		archiveDigest: metadata.archiveDigest, archiveSize: metadata.archiveSize,
		manifestDigest: metadata.manifestDigest, memberIndex: metadata.memberIndex,
		platform: metadata.platform, selector: metadata.selector, sourceReference: metadata.sourceReference,
		identity: domain.ImageIdentity{
			Origin: domain.ImageOriginDockerArchive, ReferenceDigest: metadata.referenceDigest,
			Platform: metadata.platform, PlatformManifest: metadata.platformManifest,
			ImageConfig: metadata.imageConfig,
		},
	}, true
}

func decodeArchiveMetadata(raw map[string]any) (archiveMetadata, bool) {
	var empty archiveMetadata
	archiveDigest, archiveDigestValid := digestValue(raw["archive_digest"])
	manifestDigest, manifestValid := digestValue(raw["archive_manifest_digest"])
	referenceDigest, referenceValid := digestValue(raw["reference_digest"])
	platformManifest, platformManifestValid := digestValue(raw["platform_manifest_digest"])
	imageConfig, imageConfigValid := digestValue(raw["image_config_digest"])
	if !archiveDigestValid || !manifestValid || !referenceValid ||
		!platformManifestValid || !imageConfigValid {
		return empty, false
	}

	return decodeArchiveMetadataValues(
		raw,
		archiveDigest,
		manifestDigest,
		referenceDigest,
		platformManifest,
		imageConfig,
	)
}

func decodeArchiveMetadataValues(
	raw map[string]any,
	archiveDigest, manifestDigest, referenceDigest, platformManifest, imageConfig domain.Digest,
) (archiveMetadata, bool) {
	var empty archiveMetadata
	archiveSize, sizeValid := positiveInt64(raw["archive_size"], maximumArchiveSize)
	memberIndex, indexValid := nonnegativeInt(raw["archive_member_index"], maximumArchiveMember)
	platform, platformValid := archivePlatform(stringValue(raw[archivePlatformField]))
	selector, selectorValid := raw["selector"].(string)
	sourceReference := optionalString(raw, "source_reference")
	if !sizeValid || !indexValid || !platformValid || !selectorValid || sourceReference == nil {
		return empty, false
	}

	return archiveMetadata{
		archiveDigest: archiveDigest, manifestDigest: manifestDigest,
		referenceDigest: referenceDigest, platformManifest: platformManifest,
		imageConfig: imageConfig, archiveSize: archiveSize, memberIndex: memberIndex,
		platform: platform, selector: selector, sourceReference: *sourceReference,
	}, true
}

func archiveMetadataFieldsValid(raw map[string]any) bool {
	allowed := map[string]struct{}{
		"kind": {}, "selector": {}, "archive_digest": {}, "archive_size": {},
		"archive_manifest_digest": {}, "archive_member_index": {}, archivePlatformField: {}, "source_reference": {},
		"reference_digest": {}, "platform_manifest_digest": {}, "image_config_digest": {},
	}
	if len(raw) < archiveMetadataFieldCount || len(raw) > archiveMetadataFieldCount+1 {
		return false
	}
	for key := range raw {
		if _, found := allowed[key]; !found {
			return false
		}
	}

	return true
}

func exactMapping(raw any, key string) (map[string]any, bool) {
	mapping, valid := raw.(map[string]any)
	if !valid || len(mapping) != 1 {
		return nil, false
	}
	_, found := mapping[key]

	return mapping, found
}

func stringValue(raw any) string {
	value, _ := raw.(string)

	return value
}

func optionalString(raw map[string]any, key string) *string {
	value, found := raw[key]
	if !found {
		empty := ""

		return &empty
	}
	text, valid := value.(string)
	if !valid || text == "" {
		return nil
	}

	return &text
}

func digestValue(raw any) (domain.Digest, bool) {
	value, valid := raw.(string)
	if !valid {
		return domain.Digest{}, false
	}
	digest, err := domain.ParseDigest(value)

	return digest, err == nil
}

func positiveInt64(raw any, maximum int64) (int64, bool) {
	value, valid := raw.(int)
	if !valid || value <= 0 || int64(value) > maximum {
		return 0, false
	}

	return int64(value), true
}

func nonnegativeInt(raw any, maximum int) (int, bool) {
	value, valid := raw.(int)

	return value, valid && value >= 0 && value < maximum
}

func validArchiveSelector(selector string, memberIndex int, sourceReference string) bool {
	if index, found := strings.CutPrefix(selector, "@"); found {
		parsed, err := strconv.Atoi(index)

		return err == nil && parsed == memberIndex && selector == "@"+strconv.Itoa(parsed) &&
			validOptionalSourceReference(sourceReference)
	}
	if sourceReference == "" || selector != sourceReference {
		return false
	}

	return validOptionalSourceReference(sourceReference)
}

func validOptionalSourceReference(value string) bool {
	if value == "" {
		return true
	}
	source, err := imageref.Normalize(value)

	return err == nil && !source.IsPinned() && source.String() == value &&
		strings.LastIndexByte(value, ':') > strings.LastIndexByte(value, '/')
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
