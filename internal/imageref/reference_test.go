package imageref

import (
	"errors"
	"reflect"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestAdapterPreservesDomainDigest(t *testing.T) {
	t.Parallel()

	source, err := Normalize("api:1")
	if err != nil {
		t.Fatal(err)
	}
	gotSource := []any{source.String(), source.Registry(), source.IsPinned()}
	wantSource := []any{"docker.io/library/api:1", "docker.io", false}
	if !reflect.DeepEqual(gotSource, wantSource) {
		t.Fatalf("Normalize() = %v, want %v", gotSource, wantSource)
	}
	value, err := domain.ParseDigest(testDigest)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := source.Pin(value)
	if err != nil {
		t.Fatal(err)
	}
	gotReference := []any{
		reference.String(), reference.DigestReference(), reference.Registry(), reference.Digest(),
	}
	wantReference := []any{
		"docker.io/library/api:1@" + testDigest, "docker.io/library/api@" + testDigest, "docker.io", value,
	}
	if !reflect.DeepEqual(gotReference, wantReference) {
		t.Fatalf("Pin() = %v, want %v", gotReference, wantReference)
	}
}

func TestAdapterParsesCanonicalReference(t *testing.T) {
	t.Parallel()

	value := "example.com/team/api@" + testDigest
	reference, err := Parse(value)
	if err != nil || reference.String() != value || reference.DigestReference() != value ||
		reference.Registry() != "example.com" || reference.Digest().String() != testDigest {
		t.Fatalf("Parse() = %#v, %v", reference, err)
	}
}

func TestAdapterRejectsInvalidReferences(t *testing.T) {
	t.Parallel()

	if _, err := Normalize(""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Normalize() error = %v", err)
	}
	if _, err := Parse("api:latest"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse() error = %v", err)
	}
	if _, err := (Source{}).Pin(domain.Digest{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero Source Pin() error = %v", err)
	}
	if (Reference{}).Digest() != (domain.Digest{}) {
		t.Fatalf("zero Reference Digest() = %v", (Reference{}).Digest())
	}
}
