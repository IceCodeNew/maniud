package imagearchive

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

//nolint:tagliatelle // Docker defines these legacy manifest field names.
type manifestEntry struct {
	Config       string                `json:"Config"`
	RepoTags     []string              `json:"RepoTags"`
	Layers       []string              `json:"Layers"`
	LayerSources map[string]descriptor `json:"LayerSources,omitempty"`
}

func decodeManifest(raw []byte) ([]manifestEntry, error) {
	var entries []manifestEntry
	if !utf8.Valid(raw) || !jsonstrict.Decode(bytes.NewReader(raw), maximumManifestBytes, &entries) ||
		len(entries) == 0 || len(entries) > maximumArchiveMembers {
		return nil, ErrInvalidArchive
	}

	for index := range entries {
		if !normalizeManifestEntry(&entries[index]) {
			return nil, ErrInvalidArchive
		}
	}

	return entries, nil
}

func normalizeManifestEntry(entry *manifestEntry) bool {
	if !canonicalMemberName(entry.Config) || len(entry.Layers) > maximumImageLayerCount ||
		!uniqueCanonicalMembers(entry.Layers) || !validManifestLayerSources(*entry) {
		return false
	}

	normalized := make([]string, len(entry.RepoTags))
	seen := make(map[string]struct{}, len(entry.RepoTags))
	for index, tag := range entry.RepoTags {
		canonical, valid := normalizeArchiveTag(tag)
		if !valid {
			return false
		}
		if _, duplicate := seen[canonical]; duplicate {
			return false
		}
		seen[canonical] = struct{}{}
		normalized[index] = canonical
	}
	entry.RepoTags = normalized

	return true
}

func validManifestLayerSources(entry manifestEntry) bool {
	if len(entry.LayerSources) == 0 {
		return true
	}
	if len(entry.LayerSources) > len(entry.Layers) {
		return false
	}
	for diffID, source := range entry.LayerSources {
		parsed, err := domain.ParseDigest(diffID)
		if err != nil || parsed.String() != diffID || !validLayerSourceDescriptor(source) {
			return false
		}
	}

	return true
}

func archiveBlobDigest(name string) (string, bool) {
	encoded, found := strings.CutPrefix(name, "blobs/sha256/")

	return "sha256:" + encoded, found && hexNamePattern.MatchString(encoded)
}

func manifestLayerSourcesMatch(
	entry manifestEntry,
	layers []layerIdentity,
	diffIDs []domain.Digest,
) bool {
	if len(entry.LayerSources) == 0 {
		return true
	}
	if len(layers) != len(entry.Layers) || len(diffIDs) != len(layers) {
		return false
	}
	matched := make(map[string]struct{}, len(entry.LayerSources))
	for index, diffID := range diffIDs {
		source, found := entry.LayerSources[diffID.String()]
		if !found {
			continue
		}
		matched[diffID.String()] = struct{}{}
		if !manifestLayerSourceMatches(entry.Layers[index], layers[index], diffID, source) {
			return false
		}
	}

	return len(matched) == len(entry.LayerSources)
}

func manifestLayerSourceMatches(
	name string,
	layer layerIdentity,
	diffID domain.Digest,
	source descriptor,
) bool {
	memberDigest, blob := archiveBlobDigest(name)
	if blob && memberDigest == diffID.String() && layer.digest != diffID {
		return false
	}

	return !blob || memberDigest != source.Digest ||
		source.Size == layer.size && source.Digest == layer.digest.String()
}

func normalizeArchiveTag(tag string) (string, bool) {
	if !explicitTaggedSelector(tag) {
		return "", false
	}
	source, err := imageref.Normalize(tag)
	if err != nil {
		return "", false
	}

	return source.String(), true
}

func uniqueCanonicalMembers(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !canonicalMemberName(value) {
			return false
		}
		if _, duplicate := seen[value]; duplicate {
			return false
		}
		seen[value] = struct{}{}
	}

	return true
}

func selectImage(entries []manifestEntry, selector string) (selectedImage, error) {
	selectedIndex, err := selectedManifestIndex(entries, selector)
	if err != nil {
		return selectedImage{}, err
	}
	if selectedIndex < 0 || duplicateSelectedIdentity(entries, selectedIndex) {
		return selectedImage{}, ErrInvalidArchive
	}

	return selectedImage{
		entry: entries[selectedIndex], tags: entries[selectedIndex].RepoTags, index: selectedIndex,
	}, nil
}

func selectedManifestIndex(entries []manifestEntry, selector string) (int, error) {
	if selected, found := strings.CutPrefix(selector, "@"); found {
		index, err := strconv.Atoi(selected)
		if err != nil || index < 0 || index >= len(entries) {
			return -1, ErrInvalidArchive
		}

		return index, nil
	}

	selectedIndex := -1
	for index, entry := range entries {
		if !contains(entry.RepoTags, selector) {
			continue
		}
		if selectedIndex >= 0 {
			return -1, ErrInvalidArchive
		}
		selectedIndex = index
	}

	return selectedIndex, nil
}

func duplicateSelectedIdentity(entries []manifestEntry, selected int) bool {
	config := entries[selected].Config
	tags := make(map[string]struct{}, len(entries[selected].RepoTags))
	for _, tag := range entries[selected].RepoTags {
		tags[tag] = struct{}{}
	}
	for index, entry := range entries {
		if index == selected {
			continue
		}
		if entry.Config == config {
			return true
		}
		for _, tag := range entry.RepoTags {
			if _, found := tags[tag]; found {
				return true
			}
		}
	}

	return false
}

func contains(values []string, expected string) bool {
	return slices.Contains(values, expected)
}

func selectedMembersExist(members map[string]member, entry manifestEntry) bool {
	config, found := members[entry.Config]
	if !found || config.kind != tar.TypeReg ||
		config.size <= 0 || config.size > maximumConfiguration {
		return false
	}
	for _, name := range entry.Layers {
		value, layerFound := resolvedMember(members, name)
		if !layerFound || value.kind != tar.TypeReg {
			return false
		}
	}

	return true
}

func resolvedMember(members map[string]member, name string) (member, bool) {
	value, found := members[name]
	if !found || value.kind != tar.TypeSymlink {
		return value, found
	}

	target := strings.TrimPrefix(value.link, "../")
	value, found = members[target]

	return value, found
}

func readSelected(
	ctx context.Context,
	file *os.File,
	members map[string]member,
	entry manifestEntry,
) ([]byte, []layerIdentity, error) {
	config, err := readMember(ctx, file, members[entry.Config], maximumConfiguration)
	if err != nil {
		return nil, nil, err
	}
	layers := make([]layerIdentity, len(entry.Layers))
	for index, name := range entry.Layers {
		value, _ := resolvedMember(members, name)
		digest, err := hashMember(ctx, file, value)
		if err != nil {
			return nil, nil, err
		}
		layers[index] = layerIdentity{size: value.size, digest: digest}
	}

	return config, layers, nil
}
