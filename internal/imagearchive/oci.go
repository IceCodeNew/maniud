package imagearchive

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

func archiveManifestDigest(
	config domain.Digest,
	configSize int64,
	layers []layerIdentity,
) domain.Digest {
	document := identityManifest{
		SchemaVersion: ociSchemaVersion,
		MediaType:     ociManifestMediaType,
		Config: identityDescriptor{
			MediaType: ociConfigMediaType,
			Digest:    config.String(),
			Size:      configSize,
		},
		Layers: make([]identityDescriptor, len(layers)),
	}
	for index, layer := range layers {
		document.Layers[index] = identityDescriptor{
			MediaType: ociLayerMediaType,
			Digest:    layer.digest.String(),
			Size:      layer.size,
		}
	}
	raw, _ := json.Marshal(document) //nolint:errchkjson // Fixed digest and integer fields cannot fail JSON encoding.

	return domain.Hash(raw)
}

func archiveComposeReference(selector, sourceReference string, digest domain.Digest) string {
	if !strings.HasPrefix(selector, "@") {
		return selector
	}
	if sourceReference != "" {
		return sourceReference
	}

	return archiveRepository + ":source-" + strings.TrimPrefix(digest.String(), "sha256:")
}

func validateOCIIndex(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	selectedConfig []byte,
	selectedLayers []layerIdentity,
	selectedPlatform domain.Platform,
) (domain.Digest, bool, error) {
	var empty domain.Digest
	descriptorValue, imageManifest, found, err := readOCIPlatformManifest(
		ctx,
		file,
		members,
		selectedPlatform,
	)
	if err != nil || !found {
		return empty, found, err
	}
	config, err := descriptorPayload(ctx, file, members, imageManifest.Config, maximumConfiguration)
	if err != nil || !bytes.Equal(config, selectedConfig) {
		return empty, false, ErrInvalidArchive
	}
	if err := validateOCILayers(ctx, file, members, imageManifest.Layers, selectedLayers); err != nil {
		return empty, false, err
	}
	digest, _ := domain.ParseDigest(descriptorValue.Digest) // selectOCIDescriptor validates the canonical digest.

	return digest, true, nil
}

func readOCIPlatformManifest(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	selectedPlatform domain.Platform,
) (descriptor, manifestDocument, bool, error) {
	var emptyDescriptor descriptor
	var emptyManifest manifestDocument
	indexMember, found := members[indexName]
	if !found {
		return emptyDescriptor, emptyManifest, false, nil
	}
	if indexMember.kind != tar.TypeReg {
		return emptyDescriptor, emptyManifest, false, ErrInvalidArchive
	}
	raw, err := readMember(ctx, file, indexMember, maximumManifestBytes)
	if err != nil {
		return emptyDescriptor, emptyManifest, false, err
	}
	index, err := decodeOCIIndex(raw)
	if err != nil {
		return emptyDescriptor, emptyManifest, false, err
	}
	descriptorValues, err := resolveOCIIndexDescriptors(ctx, file, members, index.Manifests)
	if err != nil {
		return emptyDescriptor, emptyManifest, false, err
	}
	descriptorValue, err := selectOCIDescriptor(descriptorValues, selectedPlatform)
	if err != nil {
		return emptyDescriptor, emptyManifest, false, err
	}
	manifest, err := descriptorPayload(ctx, file, members, descriptorValue, maximumManifestBytes)
	if err != nil {
		return emptyDescriptor, emptyManifest, false, err
	}
	imageManifest, err := decodeOCIManifest(manifest)
	if err != nil {
		return emptyDescriptor, emptyManifest, false, err
	}

	return descriptorValue, imageManifest, true, nil
}

func resolveOCIIndexDescriptors(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	values []descriptor,
) ([]descriptor, error) {
	nestedDescriptor, hasNestedIndex := nestedIndexDescriptor(values)
	if !hasNestedIndex {
		return values, nil
	}
	if !validDescriptor(nestedDescriptor, maximumManifestBytes) {
		return nil, ErrInvalidArchive
	}
	raw, err := descriptorPayload(ctx, file, members, nestedDescriptor, maximumManifestBytes)
	if err != nil {
		return nil, err
	}
	nested, err := decodeOCIIndex(raw)
	if err != nil {
		return nil, err
	}
	if containsIndexDescriptor(nested.Manifests) {
		return nil, ErrInvalidArchive
	}

	return nested.Manifests, nil
}

func nestedIndexDescriptor(values []descriptor) (descriptor, bool) {
	if len(values) != 1 || values[0].Platform != nil {
		return descriptor{}, false
	}
	value := values[0]

	return value, value.MediaType == dockerIndexMediaType || value.MediaType == ociIndexMediaType
}

func containsIndexDescriptor(values []descriptor) bool {
	for _, value := range values {
		if value.MediaType == dockerIndexMediaType || value.MediaType == ociIndexMediaType {
			return true
		}
	}

	return false
}

func decodeOCIIndex(raw []byte) (indexDocument, error) {
	var index indexDocument
	if !utf8.Valid(raw) || !jsonstrict.Decode(bytes.NewReader(raw), maximumManifestBytes, &index) {
		return indexDocument{}, ErrInvalidArchive
	}
	if !validOCIIndexEnvelope(index) {
		return indexDocument{}, ErrInvalidArchive
	}
	if len(index.Manifests) == 0 || len(index.Manifests) > maximumArchiveMembers {
		return indexDocument{}, ErrInvalidArchive
	}

	return index, nil
}

func validOCIIndexEnvelope(index indexDocument) bool {
	if index.SchemaVersion != ociSchemaVersion || index.ArtifactType != "" || index.Subject != nil {
		return false
	}
	if index.MediaType != "" && index.MediaType != dockerIndexMediaType && index.MediaType != ociIndexMediaType {
		return false
	}

	return true
}

func decodeOCIManifest(raw []byte) (manifestDocument, error) {
	var manifest manifestDocument
	if !utf8.Valid(raw) || !jsonstrict.Decode(bytes.NewReader(raw), maximumManifestBytes, &manifest) {
		return manifestDocument{}, ErrInvalidArchive
	}
	if !validOCIManifestEnvelope(manifest) || !validOCIConfigDescriptor(manifest.Config) {
		return manifestDocument{}, ErrInvalidArchive
	}
	if len(manifest.Layers) > maximumImageLayerCount {
		return manifestDocument{}, ErrInvalidArchive
	}

	return manifest, nil
}

func validOCIManifestEnvelope(manifest manifestDocument) bool {
	if manifest.SchemaVersion != ociSchemaVersion || manifest.ArtifactType != "" || manifest.Subject != nil {
		return false
	}
	if manifest.MediaType != "" && manifest.MediaType != dockerManifestMediaType &&
		manifest.MediaType != ociManifestMediaType {
		return false
	}

	return true
}

func validOCIConfigDescriptor(config descriptor) bool {
	if !validDescriptor(config, maximumConfiguration) {
		return false
	}

	return config.MediaType == dockerConfigMediaType || config.MediaType == ociConfigMediaType
}

func selectOCIDescriptor(values []descriptor, expected domain.Platform) (descriptor, error) {
	var selected descriptor
	found := false
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validImageDescriptor(value, maximumManifestBytes) || value.Platform == nil {
			return descriptor{}, ErrInvalidArchive
		}
		if _, duplicate := seen[value.Digest]; duplicate {
			return descriptor{}, ErrInvalidArchive
		}
		seen[value.Digest] = struct{}{}
		if !platformMatches(*value.Platform, expected) {
			continue
		}
		if found {
			return descriptor{}, ErrInvalidArchive
		}
		selected = value
		found = true
	}
	if !found {
		return descriptor{}, ErrInvalidArchive
	}

	return selected, nil
}

func validateOCILayers(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	layers []descriptor,
	selected []layerIdentity,
) error {
	if len(layers) != len(selected) {
		return ErrInvalidArchive
	}
	seen := make(map[string]struct{}, len(layers))
	for index, layer := range layers {
		if !validOCILayerDescriptor(layer) || layer.Size != selected[index].size ||
			layer.Digest != selected[index].digest.String() {
			return ErrInvalidArchive
		}
		if _, duplicate := seen[layer.Digest]; duplicate {
			return ErrInvalidArchive
		}
		seen[layer.Digest] = struct{}{}
		if _, err := descriptorPayload(ctx, file, members, layer, maximumArchiveBytes); err != nil {
			return err
		}
	}

	return nil
}

func validOCILayerDescriptor(value descriptor) bool {
	if !validDescriptor(value, maximumArchiveBytes) {
		return false
	}

	switch value.MediaType {
	case ociLayerMediaType, ociGzipLayerMediaType, ociZstdLayerMediaType,
		ociForeignLayerType, ociForeignGzipLayerType, ociForeignZstdLayerType,
		dockerLayerMediaType, dockerGzipLayerType, dockerForeignLayerType:
		return true
	default:
		return false
	}
}

func validImageDescriptor(value descriptor, maximum int64) bool {
	return (value.MediaType == dockerManifestMediaType || value.MediaType == ociManifestMediaType) &&
		validDescriptor(value, maximum)
}

func validDescriptor(value descriptor, maximum int64) bool {
	if value.Size <= 0 || value.Size > maximum || len(value.URLs) != 0 || len(value.Data) != 0 ||
		len(value.Digest) != len("sha256:")+64 || !strings.HasPrefix(value.Digest, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value.Digest, "sha256:"))

	return err == nil && strings.ToLower(value.Digest) == value.Digest
}

func descriptorPayload(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	value descriptor,
	maximum int64,
) ([]byte, error) {
	memberName := "blobs/sha256/" + strings.TrimPrefix(value.Digest, "sha256:")
	entry, found := members[memberName]
	if !found || entry.kind != tar.TypeReg || entry.size != value.Size {
		return nil, ErrInvalidArchive
	}
	raw, err := readMember(ctx, file, entry, maximum)
	if err != nil {
		return nil, err
	}
	if domain.Hash(raw).String() != value.Digest {
		return nil, ErrInvalidArchive
	}

	return raw, nil
}

func platformMatches(value platform, expected domain.Platform) bool {
	return value.OS == expected.OS && value.Architecture == expected.Architecture && value.Variant == expected.Variant &&
		value.OSVersion == "" && len(value.OSFeatures) == 0 && len(value.Features) == 0
}

// ImportCommand returns operator guidance. maniud never executes this command.
func (analysis Analysis) ImportCommand() string {
	platformValue := analysis.Identity.Platform.OS + "/" + analysis.Identity.Platform.Architecture
	if analysis.Identity.Platform.Variant != "" {
		platformValue += "/" + analysis.Identity.Platform.Variant
	}

	load := "docker image load --input " + shellQuote(analysis.Source.path) + " --platform " + shellQuote(platformValue)
	if analysis.SourceReference == analysis.ComposeReference {
		return load
	}

	// Docker image stores can address a loaded archive by its config or
	// manifest digest. Try each identity already verified from the archive.
	candidates := []domain.Digest{
		analysis.Identity.PlatformManifest,
		analysis.ManifestDigest,
		analysis.Identity.ImageConfig,
	}
	tagCommands := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		command := "docker image tag " + shellQuote(candidate.String()) + " " +
			shellQuote(analysis.ComposeReference)
		if !slices.Contains(tagCommands, command) {
			tagCommands = append(tagCommands, command)
		}
	}

	return load + " && (" + strings.Join(tagCommands, " || ") + ")"
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
