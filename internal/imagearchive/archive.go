// Package imagearchive validates one selected legacy Docker archive image
// without extracting or importing it.
package imagearchive

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageconfig"
)

const (
	manifestName            = "manifest.json"
	indexName               = "index.json"
	maximumManifestBytes    = int64(1 << 20)
	maximumConfiguration    = int64(16 << 20)
	maximumArchiveMembers   = 1_000_000
	maximumArchiveBytes     = int64(1 << 40)
	maximumImageLayerCount  = 1 << 16
	archiveReadBufferBytes  = 32 << 10
	tarRecordBytes          = 512
	archiveRepository       = "localhost/maniud/archive"
	linuxOperatingSystem    = "linux"
	amd64Architecture       = "amd64"
	arm64Architecture       = "arm64"
	ociSchemaVersion        = 2
	dockerManifestMediaType = "application/vnd.docker.distribution.manifest.v2+json"
	ociManifestMediaType    = "application/vnd.oci.image.manifest.v1+json"
	dockerIndexMediaType    = "application/vnd.docker.distribution.manifest.list.v2+json"
	ociIndexMediaType       = "application/vnd.oci.image.index.v1+json"
	dockerConfigMediaType   = "application/vnd.docker.container.image.v1+json"
	ociConfigMediaType      = "application/vnd.oci.image.config.v1+json"
	ociLayerMediaType       = "application/vnd.oci.image.layer.v1.tar"
	ociGzipLayerMediaType   = "application/vnd.oci.image.layer.v1.tar+gzip"
	ociZstdLayerMediaType   = "application/vnd.oci.image.layer.v1.tar+zstd"
	ociForeignLayerType     = "application/vnd.oci.image.layer.nondistributable.v1.tar"
	ociForeignGzipLayerType = "application/vnd.oci.image.layer.nondistributable.v1.tar+gzip"
	ociForeignZstdLayerType = "application/vnd.oci.image.layer.nondistributable.v1.tar+zstd"
	dockerLayerMediaType    = "application/vnd.docker.image.rootfs.diff.tar"
	dockerGzipLayerType     = "application/vnd.docker.image.rootfs.diff.tar.gzip"
	dockerForeignLayerType  = "application/vnd.docker.image.rootfs.foreign.diff.tar.gzip"
)

var (
	// ErrInvalidSource reports invalid source syntax or unsafe filesystem identity.
	ErrInvalidSource = errors.New("docker archive source is invalid")
	// ErrInvalidArchive reports an unsupported or ambiguous archive structure.
	ErrInvalidArchive = errors.New("docker archive is invalid")
)

// Analysis is the immutable image identity obtained from one archive member.
type Analysis struct {
	Source           Source
	ArchiveDigest    domain.Digest
	ArchiveSize      int64
	ManifestDigest   domain.Digest
	MemberIndex      int
	SourceReference  string
	ComposeReference string
	Identity         domain.ImageIdentity
}

type analyzeOperations struct {
	open  func(string) (*os.File, fileIdentity, error)
	close func(*os.File) error
}

type selectedImage struct {
	entry manifestEntry
	tags  []string
	index int
}

type layerIdentity struct {
	size   int64
	digest domain.Digest
}

type resolvedArchiveImage struct {
	configDigest     domain.Digest
	manifestDigest   domain.Digest
	platformManifest domain.Digest
	selected         selectedImage
	config           imageconfig.Evidence
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type descriptor struct {
	MediaType   string            `json:"mediaType"`
	Digest      string            `json:"digest"`
	Size        int64             `json:"size"`
	URLs        []string          `json:"urls,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Data        []byte            `json:"data,omitempty"`
	Platform    *platform         `json:"platform,omitempty"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type platform struct {
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
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Manifests     []descriptor      `json:"manifests"`
	Subject       *descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type manifestDocument struct {
	SchemaVersion int               `json:"schemaVersion"`
	MediaType     string            `json:"mediaType,omitempty"`
	ArtifactType  string            `json:"artifactType,omitempty"`
	Config        descriptor        `json:"config"`
	Layers        []descriptor      `json:"layers"`
	Subject       *descriptor       `json:"subject,omitempty"`
	Annotations   map[string]string `json:"annotations,omitempty"`
}

//nolint:tagliatelle // OCI defines these wire-field names.
type identityManifest struct {
	SchemaVersion int                  `json:"schemaVersion"`
	MediaType     string               `json:"mediaType"`
	Config        identityDescriptor   `json:"config"`
	Layers        []identityDescriptor `json:"layers"`
}

//nolint:tagliatelle // OCI defines these wire-field names.
type identityDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// Analyze validates and resolves one selected archive member without extraction.
func Analyze(ctx context.Context, source Source) (Analysis, error) {
	return analyzeWithOperations(ctx, source, analyzeOperations{
		open:  openSource,
		close: (*os.File).Close,
	})
}

func analyzeWithOperations(
	ctx context.Context,
	source Source,
	operations analyzeOperations,
) (Analysis, error) {
	var empty Analysis
	if err := ctx.Err(); err != nil {
		return empty, fmt.Errorf("analyze docker archive: %w", err)
	}
	if !validAbsolutePath(source.path) || !validSelector(source.selector) {
		return empty, ErrInvalidSource
	}

	file, before, err := operations.open(source.path)
	if err != nil {
		return empty, err
	}
	analysis, analyzeErr := analyzeOpenArchive(ctx, file, before, source)
	closeErr := operations.close(file)
	if analyzeErr != nil {
		return empty, errors.Join(analyzeErr, closeErr)
	}
	if closeErr != nil {
		return empty, fmt.Errorf("close docker archive: %w", closeErr)
	}

	return analysis, nil
}

func analyzeOpenArchive(
	ctx context.Context,
	file *os.File,
	before fileIdentity,
	source Source,
) (Analysis, error) {
	var empty Analysis

	members, manifest, err := scanArchive(ctx, file, before.size)
	if err != nil {
		return empty, err
	}
	entries, err := decodeManifest(manifest)
	if err != nil {
		return empty, err
	}
	if source.strictSingle && len(entries) != 1 {
		return empty, ErrInvalidArchive
	}
	selected, config, layers, configEvidence, err := analyzeSelectedImage(
		ctx, file, members, entries, source.selector,
	)
	if err != nil {
		return empty, err
	}
	platformManifest, hasOCIManifest, err := validateOCIIndex(
		ctx,
		file,
		members,
		config,
		layers,
		selected.entry,
		configEvidence.DiffIDs,
		configEvidence.Platform,
	)
	if err != nil {
		return empty, err
	}

	resolved := resolveArchiveImage(
		selected,
		config,
		layers,
		configEvidence,
		platformManifest,
		hasOCIManifest,
	)

	return finalizeAnalysis(ctx, file, before, source, resolved)
}

func resolveArchiveImage(
	selected selectedImage,
	config []byte,
	layers []layerIdentity,
	configEvidence imageconfig.Evidence,
	platformManifest domain.Digest,
	hasOCIManifest bool,
) resolvedArchiveImage {
	configDigest := domain.Hash(config)
	manifestDigest := archiveManifestDigest(configDigest, int64(len(config)), layers)
	if !hasOCIManifest {
		platformManifest = manifestDigest
	}

	return resolvedArchiveImage{
		configDigest:     configDigest,
		manifestDigest:   manifestDigest,
		platformManifest: platformManifest,
		selected:         selected,
		config:           configEvidence,
	}
}

func finalizeAnalysis(
	ctx context.Context,
	file *os.File,
	before fileIdentity,
	source Source,
	resolved resolvedArchiveImage,
) (Analysis, error) {
	var empty Analysis
	archiveDigest, err := hashArchive(ctx, file, before.size)
	if err != nil {
		return empty, err
	}
	sourceReference := firstTag(resolved.selected.tags)
	composeReference := archiveComposeReference(source.selector, sourceReference, resolved.manifestDigest)

	if err := verifySourceIdentity(file, source.path, before); err != nil {
		return empty, err
	}

	return Analysis{
		Source:           source,
		ArchiveDigest:    archiveDigest,
		ArchiveSize:      before.size,
		ManifestDigest:   resolved.manifestDigest,
		MemberIndex:      resolved.selected.index,
		SourceReference:  sourceReference,
		ComposeReference: composeReference,
		Identity: resolved.config.Identity(domain.ImageIdentity{
			Origin:           domain.ImageOriginDockerArchive,
			Reference:        composeReference,
			ReferenceDigest:  resolved.manifestDigest,
			Platform:         resolved.config.Platform,
			PlatformManifest: resolved.platformManifest,
			ImageConfig:      resolved.configDigest,
		}),
	}, nil
}

func analyzeSelectedImage(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	entries []manifestEntry,
	selector string,
) (selectedImage, []byte, []layerIdentity, imageconfig.Evidence, error) {
	selected, err := selectImage(entries, selector)
	if err != nil || !selectedMembersExist(members, selected.entry) {
		return selectedImage{}, nil, nil, imageconfig.Evidence{}, ErrInvalidArchive
	}

	config, layers, err := readSelected(ctx, file, members, selected.entry)
	if err != nil {
		return selectedImage{}, nil, nil, imageconfig.Evidence{}, err
	}
	configEvidence, err := imageconfig.Decode(config, maximumConfiguration)
	if err != nil || !supportedPlatform(configEvidence) || len(configEvidence.DiffIDs) != len(layers) {
		return selectedImage{}, nil, nil, imageconfig.Evidence{}, ErrInvalidArchive
	}
	if !manifestLayerSourcesMatch(selected.entry, layers, configEvidence.DiffIDs) {
		return selectedImage{}, nil, nil, imageconfig.Evidence{}, ErrInvalidArchive
	}

	return selected, config, layers, configEvidence, nil
}

func firstTag(tags []string) string {
	if len(tags) == 0 {
		return ""
	}

	return tags[0]
}

func supportedPlatform(value imageconfig.Evidence) bool {
	if value.OSVersion != "" || len(value.OSFeatures) != 0 || value.Platform.OS != linuxOperatingSystem {
		return false
	}

	switch value.Platform.Architecture {
	case amd64Architecture:
		return value.Platform.Variant == ""
	case arm64Architecture:
		return value.Platform.Variant == "v8"
	default:
		return false
	}
}
