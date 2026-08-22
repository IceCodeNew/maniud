package maniud

import (
	"strconv"
	"strings"

	"github.com/opencontainers/go-digest"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/imageref"
)

const (
	archiveKind                = "docker-archive"
	maximumArchiveSize         = int64(1 << 40)
	maximumArchiveMember       = 1_000_000
	archiveMetadataFieldCount  = 10
	archiveKindField           = "kind"
	archiveSelectorField       = "selector"
	archiveDigestField         = "archive_digest"
	archiveSizeField           = "archive_size"
	archiveManifestField       = "archive_manifest_digest"
	archiveMemberIndexField    = "archive_member_index"
	archivePlatformField       = "platform"
	archiveSourceRefField      = "source_reference"
	archiveReferenceField      = "reference_digest"
	archivePlatformDigestField = "platform_manifest_digest"
	archiveImageConfigField    = "image_config_digest"
	linuxOS                    = "linux"
	amd64Architecture          = "amd64"
	arm64Architecture          = "arm64"
	arm64Variant               = "v8"
	linuxAMD64Platform         = "linux/amd64"
	linuxARM64Platform         = "linux/arm64/v8"
)

// ArchiveProof is the path-free identity evidence embedded for an analyzed
// Docker archive. It does not grant ownership or authorize a runtime effect.
type ArchiveProof struct {
	ArchiveDigest          digest.Digest
	ArchiveSize            int64
	ManifestDigest         digest.Digest
	MemberIndex            int
	Platform               containerconfig.Platform
	Selector               string
	SourceReference        string
	ReferenceDigest        digest.Digest
	PlatformManifestDigest digest.Digest
	ImageConfigDigest      digest.Digest
}

func decodeArchiveProof(raw any) (ArchiveProof, error) {
	value, valid := raw.(map[string]any)
	if !valid || !archiveMetadataFieldsValid(value) || stringValue(value[archiveKindField]) != archiveKind {
		return ArchiveProof{}, ErrInvalid
	}
	proof, valid := decodeArchiveDigests(value)
	if !valid {
		return ArchiveProof{}, ErrInvalid
	}
	proof, valid = decodeArchiveValues(value, proof)
	if !valid || !validArchiveProof(proof) {
		return ArchiveProof{}, ErrInvalid
	}

	return proof, nil
}

func decodeArchiveDigests(value map[string]any) (ArchiveProof, bool) {
	proof := ArchiveProof{}
	var valid bool
	proof.ArchiveDigest, valid = digestValue(value[archiveDigestField])
	if !valid {
		return ArchiveProof{}, false
	}
	proof.ManifestDigest, valid = digestValue(value[archiveManifestField])
	if !valid {
		return ArchiveProof{}, false
	}
	proof.ReferenceDigest, valid = digestValue(value[archiveReferenceField])
	if !valid {
		return ArchiveProof{}, false
	}
	proof.PlatformManifestDigest, valid = digestValue(value[archivePlatformDigestField])
	if !valid {
		return ArchiveProof{}, false
	}
	proof.ImageConfigDigest, valid = digestValue(value[archiveImageConfigField])
	if !valid {
		return ArchiveProof{}, false
	}

	return proof, true
}

func decodeArchiveValues(value map[string]any, proof ArchiveProof) (ArchiveProof, bool) {
	var valid bool
	proof.ArchiveSize, valid = positiveInt64(value[archiveSizeField], maximumArchiveSize)
	if !valid {
		return ArchiveProof{}, false
	}
	proof.MemberIndex, valid = nonnegativeInt(value[archiveMemberIndexField], maximumArchiveMember)
	if !valid {
		return ArchiveProof{}, false
	}
	proof.Platform, valid = parsePlatform(stringValue(value[archivePlatformField]))
	if !valid {
		return ArchiveProof{}, false
	}
	proof.Selector, valid = value[archiveSelectorField].(string)
	if !valid {
		return ArchiveProof{}, false
	}
	sourceReference := optionalString(value, archiveSourceRefField)
	if sourceReference == nil {
		return ArchiveProof{}, false
	}
	proof.SourceReference = *sourceReference

	return proof, true
}

func encodeArchiveProof(proof ArchiveProof) (map[string]any, error) {
	if !validArchiveProof(proof) {
		return nil, ErrInvalid
	}
	value := map[string]any{
		archiveKindField:           archiveKind,
		archiveSelectorField:       proof.Selector,
		archiveDigestField:         proof.ArchiveDigest.String(),
		archiveSizeField:           int(proof.ArchiveSize),
		archiveManifestField:       proof.ManifestDigest.String(),
		archiveMemberIndexField:    proof.MemberIndex,
		archivePlatformField:       formatPlatform(proof.Platform),
		archiveReferenceField:      proof.ReferenceDigest.String(),
		archivePlatformDigestField: proof.PlatformManifestDigest.String(),
		archiveImageConfigField:    proof.ImageConfigDigest.String(),
	}
	if proof.SourceReference != "" {
		value[archiveSourceRefField] = proof.SourceReference
	}

	return value, nil
}

func validArchiveProof(proof ArchiveProof) bool {
	return validArchiveDigests(proof) && validArchiveCoordinates(proof) &&
		proof.ManifestDigest == proof.ReferenceDigest
}

func validArchiveDigests(proof ArchiveProof) bool {
	return validDigest(proof.ArchiveDigest) && validDigest(proof.ManifestDigest) &&
		validDigest(proof.ReferenceDigest) && validDigest(proof.PlatformManifestDigest) &&
		validDigest(proof.ImageConfigDigest)
}

func validArchiveCoordinates(proof ArchiveProof) bool {
	return proof.ArchiveSize > 0 && proof.ArchiveSize <= maximumArchiveSize &&
		proof.MemberIndex >= 0 && proof.MemberIndex < maximumArchiveMember &&
		supportedPlatform(proof.Platform) &&
		validArchiveSelector(proof.Selector, proof.MemberIndex, proof.SourceReference)
}

func validDigest(value digest.Digest) bool {
	return value.Algorithm() == digest.SHA256 && value.Validate() == nil &&
		value.String() == strings.ToLower(value.String())
}

func archiveMetadataFieldsValid(raw map[string]any) bool {
	allowed := map[string]struct{}{
		archiveKindField: {}, archiveSelectorField: {}, archiveDigestField: {}, archiveSizeField: {},
		archiveManifestField: {}, archiveMemberIndexField: {}, archivePlatformField: {}, archiveSourceRefField: {},
		archiveReferenceField: {}, archivePlatformDigestField: {}, archiveImageConfigField: {},
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

func digestValue(raw any) (digest.Digest, bool) {
	value, valid := raw.(string)
	if !valid {
		return "", false
	}
	parsed, err := digest.Parse(value)

	return parsed, err == nil && validDigest(parsed)
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

func stringValue(raw any) string {
	value, _ := raw.(string)

	return value
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

func parsePlatform(value string) (containerconfig.Platform, bool) {
	switch value {
	case linuxAMD64Platform:
		return containerconfig.Platform{OS: linuxOS, Architecture: amd64Architecture}, true
	case linuxARM64Platform:
		return containerconfig.Platform{OS: linuxOS, Architecture: arm64Architecture, Variant: arm64Variant}, true
	default:
		return containerconfig.Platform{}, false
	}
}

func formatPlatform(value containerconfig.Platform) string {
	formatted := value.OS + "/" + value.Architecture
	if value.Variant != "" {
		formatted += "/" + value.Variant
	}

	return formatted
}

func supportedPlatform(value containerconfig.Platform) bool {
	_, valid := parsePlatform(formatPlatform(value))

	return valid
}
