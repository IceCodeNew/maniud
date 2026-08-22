// Package registry resolves registry image sources to verified immutable identities.
package registry

import (
	"bytes"
	"context"
	"math"
	"slices"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageconfig"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	maximumConfigBytes          = int64(8 << 20)
	maximumManifestBytes        = int64(8 << 20)
	dockerMediaTypeImageConfig  = "application/vnd.docker.container.image.v1+json"
	dockerMediaTypeManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	dockerMediaTypeManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	maximumImageLayers          = 4096
)

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type descriptor struct {
	MediaType    string            `json:"mediaType"`
	Digest       digest.Digest     `json:"digest"`
	Size         int64             `json:"size"`
	URLs         []string          `json:"urls,omitempty"`
	Annotations  map[string]string `json:"annotations,omitempty"`
	Data         []byte            `json:"data,omitempty"`
	Platform     *imagePlatform    `json:"platform,omitempty"`
	ArtifactType string            `json:"artifactType,omitempty"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type imagePlatform struct {
	Architecture string   `json:"architecture"`
	OS           string   `json:"os"`
	OSVersion    string   `json:"os.version,omitempty"`
	OSFeatures   []string `json:"os.features,omitempty"`
	Variant      string   `json:"variant,omitempty"`
	Features     []string `json:"features,omitempty"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type indexDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Manifests     []descriptor      `json:"manifests"`
	Subject       *descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type imageManifestDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Subject       *descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

type manifestDocument struct {
	descriptor ocispec.Descriptor
	digest     domain.Digest
	manifests  []descriptor
	config     *descriptor
	layers     []descriptor
}

type imageConfigEvidence struct {
	platform      imagePlatform
	configuration imageconfig.Evidence
}

func decodeManifest(raw []byte, descriptorValue ocispec.Descriptor) (manifestDocument, error) {
	var empty manifestDocument

	digest, valid := validRawDescriptor(descriptorValue, raw, maximumManifestBytes)
	if !valid {
		return empty, ErrProtocol
	}

	switch descriptorValue.MediaType {
	case dockerMediaTypeManifestList, ocispec.MediaTypeImageIndex:
		manifests, err := decodeIndex(raw, descriptorValue.MediaType)
		if err != nil {
			return empty, ErrProtocol
		}

		var document manifestDocument

		document.descriptor = descriptorValue
		document.digest = digest
		document.manifests = manifests

		return document, nil
	case dockerMediaTypeManifest, ocispec.MediaTypeImageManifest:
		config, layers, err := decodeImageManifest(raw, descriptorValue.MediaType)
		if err != nil {
			return empty, ErrProtocol
		}

		var document manifestDocument

		document.descriptor = descriptorValue
		document.digest = digest
		document.config = &config
		document.layers = layers

		return document, nil
	default:
		return empty, ErrProtocol
	}
}

func decodeIndex(raw []byte, mediaType string) ([]descriptor, error) {
	var parsed indexDocument
	if !jsonstrict.Decode(bytes.NewReader(raw), maximumManifestBytes, &parsed) || parsed.SchemaVersion != 2 ||
		parsed.MediaType != "" && parsed.MediaType != mediaType || parsed.ArtifactType != "" || parsed.Subject != nil {
		return nil, ErrProtocol
	}

	return parsed.Manifests, nil
}

func decodeImageManifest(raw []byte, mediaType string) (descriptor, []descriptor, error) {
	var parsed imageManifestDocument
	if !jsonstrict.Decode(bytes.NewReader(raw), maximumManifestBytes, &parsed) || parsed.SchemaVersion != 2 ||
		parsed.MediaType != "" && parsed.MediaType != mediaType || parsed.ArtifactType != "" || parsed.Subject != nil ||
		len(parsed.Layers) > maximumImageLayers {
		return descriptor{}, nil, ErrProtocol
	}

	return parsed.Config, parsed.Layers, nil
}

func verifyLocalLayers(ctx context.Context, repository LocalRepository, layers []descriptor) error {
	if repository == nil {
		return nil
	}

	for _, layer := range layers {
		if layer.Platform != nil || !validLayerDescriptor(layer) {
			return ErrProtocol
		}
		if err := repository.Verify(ctx, toOCIDescriptor(layer)); err != nil {
			return classifyRemoteError(err)
		}
	}

	return nil
}

func validLayerDescriptor(value descriptor) bool {
	if !validLayerMediaType(value.MediaType) {
		return false
	}

	return validDescriptor(value, math.MaxInt64, value.MediaType)
}

func validLayerMediaType(value string) bool {
	switch value {
	case "application/vnd.oci.image.layer.v1.tar",
		"application/vnd.oci.image.layer.v1.tar+gzip",
		"application/vnd.oci.image.layer.v1.tar+zstd",
		"application/vnd.oci.image.layer.nondistributable.v1.tar",
		"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip",
		"application/vnd.oci.image.layer.nondistributable.v1.tar+zstd",
		"application/vnd.docker.image.rootfs.diff.tar",
		"application/vnd.docker.image.rootfs.diff.tar.gzip",
		"application/vnd.docker.image.rootfs.foreign.diff.tar",
		"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip":
		return true
	default:
		return false
	}
}

func validRawDescriptor(value ocispec.Descriptor, raw []byte, maximum int64) (domain.Digest, bool) {
	digest, err := domain.ParseDigest(value.Digest.String())
	if err != nil || value.Size <= 0 || value.Size > maximum || value.Size != int64(len(raw)) ||
		len(value.URLs) != 0 || len(value.Data) != 0 || value.ArtifactType != "" || domain.Hash(raw) != digest {
		return domain.Digest{}, false
	}

	return digest, true
}

func validDescriptor(value descriptor, maximum int64, mediaTypes ...string) bool {
	_, err := domain.ParseDigest(value.Digest.String())
	if err != nil || value.Size <= 0 || value.Size > maximum || len(value.URLs) != 0 || len(value.Data) != 0 ||
		value.ArtifactType != "" || !slices.Contains(mediaTypes, value.MediaType) {
		return false
	}

	return true
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

func decodeImageConfig(
	raw []byte,
	descriptorValue ocispec.Descriptor,
) (imageConfigEvidence, domain.Digest, error) {
	digest, valid := validRawDescriptor(descriptorValue, raw, maximumConfigBytes)
	if !valid || !slices.Contains(
		[]string{dockerMediaTypeImageConfig, ocispec.MediaTypeImageConfig},
		descriptorValue.MediaType,
	) {
		return imageConfigEvidence{}, domain.Digest{}, ErrProtocol
	}

	parsed, err := imageconfig.Decode(raw, maximumConfigBytes)
	if err != nil {
		return imageConfigEvidence{}, domain.Digest{}, ErrProtocol
	}

	platform, err := normalizePlatform(domain.Platform{
		OS:           parsed.Platform.OS,
		Architecture: parsed.Platform.Architecture,
		Variant:      parsed.Platform.Variant,
	})
	if err != nil || platform.OSVersion != parsed.OSVersion || len(parsed.OSFeatures) != 0 {
		return imageConfigEvidence{}, domain.Digest{}, ErrProtocol
	}

	return imageConfigEvidence{
		platform: platform, configuration: parsed,
	}, digest, nil
}
