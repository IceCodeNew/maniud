package registry

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	dockerMediaTypeImageConfig  = "application/vnd.docker.container.image.v1+json"
	dockerMediaTypeManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	dockerMediaTypeManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
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
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type imageConfig struct {
	Architecture    string          `json:"architecture"`
	Author          string          `json:"author,omitempty"`
	Config          json.RawMessage `json:"config,omitempty"`
	Container       string          `json:"container,omitempty"`
	ContainerConfig json.RawMessage `json:"container_config,omitempty"`
	Created         json.RawMessage `json:"created,omitempty"`
	DockerVersion   string          `json:"docker_version,omitempty"`
	History         json.RawMessage `json:"history,omitempty"`
	OS              string          `json:"os"`
	OSFeatures      []string        `json:"os.features,omitempty"`
	OSVersion       string          `json:"os.version,omitempty"`
	RootFS          json.RawMessage `json:"rootfs"`
	Variant         string          `json:"variant,omitempty"`
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
		config, err := decodeImageManifest(raw, descriptorValue.MediaType)
		if err != nil {
			return empty, ErrProtocol
		}

		var document manifestDocument

		document.descriptor = descriptorValue
		document.digest = digest
		document.config = &config

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

func decodeImageManifest(raw []byte, mediaType string) (descriptor, error) {
	var parsed imageManifestDocument
	if !jsonstrict.Decode(bytes.NewReader(raw), maximumManifestBytes, &parsed) || parsed.SchemaVersion != 2 ||
		parsed.MediaType != "" && parsed.MediaType != mediaType || parsed.ArtifactType != "" || parsed.Subject != nil {
		return descriptor{}, ErrProtocol
	}

	return parsed.Config, nil
}

func acceptedManifestMediaTypes() []string {
	return []string{
		dockerMediaTypeManifest,
		dockerMediaTypeManifestList,
		ocispec.MediaTypeImageManifest,
		ocispec.MediaTypeImageIndex,
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

func decodeImageConfig(raw []byte, descriptorValue ocispec.Descriptor) (imagePlatform, domain.Digest, error) {
	var parsed imageConfig

	digest, valid := validRawDescriptor(descriptorValue, raw, maximumConfigBytes)
	if !valid || !slices.Contains(
		[]string{dockerMediaTypeImageConfig, ocispec.MediaTypeImageConfig},
		descriptorValue.MediaType,
	) || !jsonstrict.Decode(bytes.NewReader(raw), maximumConfigBytes, &parsed) {
		return imagePlatform{}, domain.Digest{}, ErrProtocol
	}

	platform, err := normalizePlatform(domain.Platform{
		OS:           parsed.OS,
		Architecture: parsed.Architecture,
		Variant:      parsed.Variant,
	})
	if err != nil || platform.OSVersion != parsed.OSVersion || len(parsed.OSFeatures) != 0 {
		return imagePlatform{}, domain.Digest{}, ErrProtocol
	}

	return platform, digest, nil
}
