// Package imageref owns maniud's canonical immutable registry-reference grammar.
package imageref

import (
	"errors"
	"strings"

	"github.com/regclient/regclient/types/ref"

	"github.com/IceCodeNew/maniud/internal/domain"
)

// ErrInvalid reports a registry reference outside maniud's canonical pinned grammar.
var ErrInvalid = errors.New("image reference is invalid")

// Reference is a canonical registry image reference with an immutable SHA-256 digest.
type Reference struct {
	value           string
	digestReference string
	registry        string
	digest          domain.Digest
}

// Parse requires an explicit lowercase registry and canonical digest-pinned reference.
func Parse(value string) (Reference, error) {
	var empty Reference

	parsed, err := ref.New(value)
	if err != nil || parsed.CommonName() != value {
		return empty, ErrInvalid
	}

	repositorySeparator := strings.IndexByte(value, '/')

	digestSeparator := strings.LastIndexByte(value, '@')
	if repositorySeparator <= 0 || digestSeparator <= repositorySeparator ||
		value[:repositorySeparator] != strings.ToLower(value[:repositorySeparator]) {
		return empty, ErrInvalid
	}

	digest, err := domain.ParseDigest(value[digestSeparator+1:])
	if err != nil {
		return empty, ErrInvalid
	}

	digestReference := value

	tagSeparator := strings.LastIndexByte(value[repositorySeparator+1:digestSeparator], ':')
	if tagSeparator >= 0 {
		tagSeparator += repositorySeparator + 1
		digestReference = value[:tagSeparator] + value[digestSeparator:]
	}

	return Reference{
		value:           value,
		digestReference: digestReference,
		registry:        value[:repositorySeparator],
		digest:          digest,
	}, nil
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
