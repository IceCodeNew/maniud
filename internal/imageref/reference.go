// Package imageref owns maniud's registry image-reference grammar.
package imageref

import (
	"errors"
	"strings"

	orasregistry "oras.land/oras-go/v2/registry"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	dockerRegistry        = "docker.io"
	dockerRegistryAlias   = "index.docker.io"
	dockerRegistryHost    = "registry-1.docker.io"
	dockerLibraryPrefix   = "library/"
	defaultImageReference = "latest"
)

// ErrInvalid reports an invalid registry image reference.
var ErrInvalid = errors.New("image reference is invalid")

// Source is a normalized registry image source that may require tag resolution.
type Source struct {
	value    string
	registry string
	digest   domain.Digest
	pinned   bool
}

// Reference is a canonical registry image reference with an immutable SHA-256 digest.
type Reference struct {
	value           string
	digestReference string
	registry        string
	digest          domain.Digest
}

// Normalize accepts Docker-compatible registry input and returns its canonical name.
func Normalize(value string) (Source, error) {
	var empty Source

	nameAndTag, digestText, hasDigest, valid := splitDigest(value)
	if !valid {
		return empty, ErrInvalid
	}

	name, tag, hasTag := splitTag(nameAndTag)

	registryName, repository, valid := splitRepository(name)
	if !valid {
		return empty, ErrInvalid
	}

	if !hasTag && !hasDigest {
		tag = defaultImageReference
		hasTag = true
	}

	if !validORASReference(registryName, repository, tag, hasTag) {
		return empty, ErrInvalid
	}

	normalized := registryName + "/" + repository
	if hasTag {
		normalized += ":" + tag
	}

	source := Source{
		value:    normalized,
		registry: registryName,
		digest:   domain.Digest{},
		pinned:   false,
	}

	if !hasDigest {
		return source, nil
	}

	digest, err := domain.ParseDigest(digestText)
	if err != nil {
		return empty, ErrInvalid
	}

	source.value += "@" + digest.String()
	source.digest = digest
	source.pinned = true

	return source, nil
}

func splitDigest(value string) (string, string, bool, bool) {
	nameAndTag, digestText, hasDigest := strings.Cut(value, "@")
	valid := value != "" && strings.TrimSpace(value) == value && nameAndTag != "" &&
		(!hasDigest || digestText != "" && !strings.ContainsRune(digestText, '@'))

	return nameAndTag, digestText, hasDigest, valid
}

func splitTag(value string) (string, string, bool) {
	tagSeparator := strings.LastIndexByte(value, ':')
	if tagSeparator <= strings.LastIndexByte(value, '/') {
		return value, "", false
	}

	return value[:tagSeparator], value[tagSeparator+1:], true
}

func splitRepository(value string) (string, string, bool) {
	first, rest, hasSlash := strings.Cut(value, "/")
	registryName := strings.ToLower(first)
	repository := rest

	if !hasSlash || !isRegistry(first) {
		registryName = dockerRegistry
		repository = value
	}

	if registryName == dockerRegistryAlias || registryName == dockerRegistryHost {
		registryName = dockerRegistry
	}

	if registryName == dockerRegistry && !strings.ContainsRune(repository, '/') {
		repository = dockerLibraryPrefix + repository
	}

	return registryName, repository, repository != "" && repository == strings.ToLower(repository)
}

func isRegistry(value string) bool {
	return strings.ContainsAny(value, ".:") || value == "localhost" || strings.ToLower(value) != value
}

func validORASReference(registryName, repository, tag string, hasTag bool) bool {
	parsed := orasregistry.Reference{
		Registry:   registryName,
		Repository: repository,
		Reference:  "",
	}
	if hasTag {
		parsed.Reference = tag
	}

	return parsed.Validate() == nil
}

// Parse requires an explicit lowercase registry and canonical digest-pinned reference.
func Parse(value string) (Reference, error) {
	var empty Reference

	source, err := Normalize(value)
	if err != nil || source.String() != value || !source.IsPinned() {
		return empty, ErrInvalid
	}

	return source.reference(), nil
}

// String returns the normalized source reference.
func (source Source) String() string {
	return source.value
}

// Registry returns the normalized registry authority.
func (source Source) Registry() string {
	return source.registry
}

// IsPinned reports whether the source already includes an immutable digest.
func (source Source) IsPinned() bool {
	return source.pinned
}

// Pin adds a resolved digest while preserving any informational tag.
func (source Source) Pin(digest domain.Digest) (Reference, error) {
	if source.value == "" || source.pinned && source.digest != digest {
		return Reference{}, ErrInvalid
	}

	if source.pinned {
		return source.reference(), nil
	}

	pinned := source
	pinned.value += "@" + digest.String()
	pinned.digest = digest
	pinned.pinned = true

	return pinned.reference(), nil
}

func (source Source) reference() Reference {
	digestSeparator := strings.LastIndexByte(source.value, '@')
	digestReference := source.value

	tagSeparator := strings.LastIndexByte(source.value[:digestSeparator], ':')
	if tagSeparator > strings.IndexByte(source.value, '/') {
		digestReference = source.value[:tagSeparator] + source.value[digestSeparator:]
	}

	return Reference{
		value:           source.value,
		digestReference: digestReference,
		registry:        source.registry,
		digest:          source.digest,
	}
}

// String returns the canonical immutable reference.
func (reference Reference) String() string {
	return reference.value
}

// DigestReference returns the canonical reference without an informational tag.
func (reference Reference) DigestReference() string {
	return reference.digestReference
}

// Registry returns the explicit registry authority.
func (reference Reference) Registry() string {
	return reference.registry
}

// Digest returns the immutable repository reference digest.
func (reference Reference) Digest() domain.Digest {
	return reference.digest
}
