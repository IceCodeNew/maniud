// Package imageref normalizes Docker-compatible registry image references.
package imageref

import (
	"errors"
	"strings"

	distributionref "github.com/distribution/reference"
	"github.com/opencontainers/go-digest"
)

const (
	dockerRegistry      = "docker.io"
	dockerRegistryAlias = "index.docker.io"
	dockerRegistryHost  = "registry-1.docker.io"
)

// ErrInvalid reports an image reference outside the supported canonical
// Docker-compatible and SHA-256 contract.
var ErrInvalid = errors.New("image reference is invalid")

// Source is a normalized registry image source. It may use a mutable tag or
// already include an immutable SHA-256 digest.
type Source struct {
	value      string
	registry   string
	repository string
	tag        string
	digest     digest.Digest
}

// Reference is a canonical registry image reference with an immutable
// SHA-256 digest. An informational tag, when present, remains in String.
type Reference struct {
	value           string
	digestReference string
	registry        string
	repository      string
	tag             string
	digest          digest.Digest
}

// Normalize accepts Docker-compatible familiar names and returns a fully
// qualified reference. It adds Docker Hub's library namespace and latest tag,
// canonicalizes known Docker Hub hosts, and lowercases registry authorities.
func Normalize(value string) (Source, error) {
	var empty Source

	if value == "" || strings.TrimSpace(value) != value || strings.Count(value, "@") > 1 {
		return empty, ErrInvalid
	}
	named, err := distributionref.ParseNormalizedNamed(canonicalizeRegistry(value))
	if err != nil {
		return empty, ErrInvalid
	}
	named = distributionref.TagNameOnly(named)
	digested, pinned := named.(distributionref.Digested)
	if pinned && !validDigest(digested.Digest()) {
		return empty, ErrInvalid
	}

	source := Source{
		value:      named.String(),
		registry:   distributionref.Domain(named),
		repository: distributionref.Path(named),
	}
	if tagged, ok := named.(distributionref.Tagged); ok {
		source.tag = tagged.Tag()
	}
	if pinned {
		source.digest = digested.Digest()
	}

	return source, nil
}

func canonicalizeRegistry(value string) string {
	first, rest, found := strings.Cut(value, "/")
	if !found || !isRegistry(first) {
		return value
	}
	registry := strings.ToLower(first)
	if registry == dockerRegistryAlias || registry == dockerRegistryHost {
		registry = dockerRegistry
	}

	return registry + "/" + rest
}

func isRegistry(value string) bool {
	return strings.ContainsAny(value, ".:") || value == "localhost" || strings.ToLower(value) != value
}

func validDigest(value digest.Digest) bool {
	return value.Algorithm() == digest.SHA256 && value.Validate() == nil &&
		value.String() == strings.ToLower(value.String())
}

// Parse requires an explicit lowercase registry and canonical digest-pinned
// reference.
func Parse(value string) (Reference, error) {
	source, err := Normalize(value)
	if err != nil || source.String() != value || !source.IsPinned() {
		return Reference{}, ErrInvalid
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

// Repository returns the lowercase repository path without the registry.
func (source Source) Repository() string {
	return source.repository
}

// Tag returns the informational tag, or an empty string when the source only
// has a digest.
func (source Source) Tag() string {
	return source.tag
}

// IsPinned reports whether the source already includes an immutable digest.
func (source Source) IsPinned() bool {
	return source.digest != ""
}

// Pin adds a resolved SHA-256 digest while preserving any informational tag.
func (source Source) Pin(value digest.Digest) (Reference, error) {
	if source.value == "" || !validDigest(value) || source.IsPinned() && source.digest != value {
		return Reference{}, ErrInvalid
	}
	if source.IsPinned() {
		return source.reference(), nil
	}

	source.value += "@" + value.String()
	source.digest = value

	return source.reference(), nil
}

func (source Source) reference() Reference {
	return Reference{
		value:           source.value,
		digestReference: source.registry + "/" + source.repository + "@" + source.digest.String(),
		registry:        source.registry,
		repository:      source.repository,
		tag:             source.tag,
		digest:          source.digest,
	}
}

// String returns the canonical immutable reference, preserving an
// informational tag when one was supplied.
func (reference Reference) String() string {
	return reference.value
}

// DigestReference returns the immutable reference without an informational
// tag.
func (reference Reference) DigestReference() string {
	return reference.digestReference
}

// Registry returns the normalized registry authority.
func (reference Reference) Registry() string {
	return reference.registry
}

// Repository returns the lowercase repository path without the registry.
func (reference Reference) Repository() string {
	return reference.repository
}

// Tag returns the informational tag, or an empty string when none was
// supplied.
func (reference Reference) Tag() string {
	return reference.tag
}

// Digest returns the immutable SHA-256 repository digest.
func (reference Reference) Digest() digest.Digest {
	return reference.digest
}
