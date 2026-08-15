package imageref

import (
	"errors"
	"strings"
	"testing"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestParseCanonicalReference(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"example.com/team/api@" + testDigest,
		"localhost:5000/team/api@" + testDigest,
		"[::1]:5000/team/api@" + testDigest,
	} {
		reference, err := Parse(value)
		if err != nil {
			t.Fatalf("Parse(%q) error = %v", value, err)
		}

		wantRegistry := value[:strings.IndexByte(value, '/')]
		if reference.String() != value || reference.DigestReference() != value || reference.Registry() != wantRegistry ||
			reference.Digest().String() != testDigest {
			t.Fatalf("Parse(%q) = %#v", value, reference)
		}
	}
}

func TestParsePreservesTagAsMetadata(t *testing.T) {
	t.Parallel()

	value := "example.com/team/api:1@" + testDigest

	reference, err := Parse(value)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", value, err)
	}

	wantDigestReference := "example.com/team/api@" + testDigest
	if reference.String() != value || reference.DigestReference() != wantDigestReference {
		t.Fatalf("Parse(%q) = %#v", value, reference)
	}
}

func TestParseRejectsNonCanonicalReference(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"",
		"api@" + testDigest,
		"team/api@" + testDigest,
		"EXAMPLE.com/team/api@" + testDigest,
		"example.com/team/API@" + testDigest,
		"example.com/team/api:latest",
		"docker.io/api@" + testDigest,
		"https://example.com/team/api@" + testDigest,
		"example.com/team/api@sha256:" + strings.Repeat("A", 64),
		"example.com/team/api@sha512:" + strings.Repeat("0", 128),
	} {
		_, err := Parse(value)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Parse(%q) error = %v, want ErrInvalid", value, err)
		}
	}
}
