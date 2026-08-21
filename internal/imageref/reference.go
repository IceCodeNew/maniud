// Package imageref adapts public image references to maniud's fixed-size
// domain digest representation.
package imageref

import (
	"github.com/opencontainers/go-digest"

	publicref "github.com/IceCodeNew/maniud/imageref"
	"github.com/IceCodeNew/maniud/internal/domain"
)

// ErrInvalid reports an invalid registry image reference.
var ErrInvalid = publicref.ErrInvalid

// Source is a normalized registry image source that may require tag
// resolution.
type Source struct {
	value publicref.Source
}

// Reference is a canonical registry image reference with an immutable
// SHA-256 digest.
type Reference struct {
	value publicref.Reference
}

// Normalize accepts Docker-compatible registry input and returns its
// canonical name.
func Normalize(value string) (Source, error) {
	reference, err := publicref.Normalize(value)
	if err != nil {
		return Source{}, ErrInvalid
	}

	return Source{value: reference}, nil
}

// Parse requires an explicit lowercase registry and canonical digest-pinned
// reference.
func Parse(value string) (Reference, error) {
	reference, err := publicref.Parse(value)
	if err != nil {
		return Reference{}, ErrInvalid
	}

	return Reference{value: reference}, nil
}

// String returns the normalized source reference.
func (source Source) String() string {
	return source.value.String()
}

// Registry returns the normalized registry authority.
func (source Source) Registry() string {
	return source.value.Registry()
}

// IsPinned reports whether the source already includes an immutable digest.
func (source Source) IsPinned() bool {
	return source.value.IsPinned()
}

// Pin adds a resolved digest while preserving any informational tag.
func (source Source) Pin(value domain.Digest) (Reference, error) {
	reference, err := source.value.Pin(digest.Digest(value.String()))
	if err != nil {
		return Reference{}, ErrInvalid
	}

	return Reference{value: reference}, nil
}

// String returns the canonical immutable reference.
func (reference Reference) String() string {
	return reference.value.String()
}

// DigestReference returns the canonical reference without an informational
// tag.
func (reference Reference) DigestReference() string {
	return reference.value.DigestReference()
}

// Registry returns the explicit registry authority.
func (reference Reference) Registry() string {
	return reference.value.Registry()
}

// Digest returns the immutable repository reference digest.
func (reference Reference) Digest() domain.Digest {
	// The public reference constructor guarantees canonical SHA-256. Keep the
	// conversion private so project digest storage does not leak into its API.
	value, _ := domain.ParseDigest(reference.value.Digest().String())

	return value
}
