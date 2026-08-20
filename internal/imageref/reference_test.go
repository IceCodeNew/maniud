package imageref

import (
	"errors"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testDigest         = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testDockerRegistry = "docker.io"
	testDockerLatest   = "docker.io/library/api:latest"
)

func TestNormalizeDockerCompatibleInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    string
		want     string
		registry string
		pinned   bool
	}{
		{input: "api", want: testDockerLatest, registry: testDockerRegistry, pinned: false},
		{input: "library/api", want: testDockerLatest, registry: testDockerRegistry, pinned: false},
		{input: "docker.io/api:1", want: "docker.io/library/api:1", registry: testDockerRegistry, pinned: false},
		{input: "index.docker.io/api", want: testDockerLatest, registry: testDockerRegistry, pinned: false},
		{
			input:    "registry-1.docker.io/api",
			want:     testDockerLatest,
			registry: testDockerRegistry,
			pinned:   false,
		},
		{input: "EXAMPLE.com/team/api:1", want: "example.com/team/api:1", registry: "example.com", pinned: false},
		{input: "localhost:5000/team/api", want: "localhost:5000/team/api:latest", registry: "localhost:5000", pinned: false},
		{
			input:    "api@" + testDigest,
			want:     "docker.io/library/api@" + testDigest,
			registry: testDockerRegistry,
			pinned:   true,
		},
		{
			input:    "api:1@" + testDigest,
			want:     "docker.io/library/api:1@" + testDigest,
			registry: testDockerRegistry,
			pinned:   true,
		},
	}

	for _, test := range tests {
		source, err := Normalize(test.input)
		if err != nil {
			t.Fatalf("Normalize(%q) error = %v", test.input, err)
		}

		if source.String() != test.want || source.Registry() != test.registry || source.IsPinned() != test.pinned {
			t.Fatalf("Normalize(%q) = %#v", test.input, source)
		}
	}
}

func TestSourcePinPreservesUpdateMetadata(t *testing.T) {
	t.Parallel()

	digest, err := domain.ParseDigest(testDigest)
	if err != nil {
		t.Fatalf("ParseDigest(testDigest) error = %v", err)
	}

	for _, test := range []struct {
		input string
		want  string
	}{
		{input: "api", want: "docker.io/library/api:latest@" + testDigest},
		{input: "api:1", want: "docker.io/library/api:1@" + testDigest},
		{input: "api@" + testDigest, want: "docker.io/library/api@" + testDigest},
	} {
		source, normalizeErr := Normalize(test.input)
		if normalizeErr != nil {
			t.Fatalf("Normalize(%q) error = %v", test.input, normalizeErr)
		}

		reference, pinErr := source.Pin(digest)
		if pinErr != nil {
			t.Fatalf("Pin(%q) error = %v", test.input, pinErr)
		}

		if reference.String() != test.want || reference.Digest().String() != testDigest {
			t.Fatalf("Pin(%q) = %#v", test.input, reference)
		}
	}
}

func TestSourcePinRejectsConflictingDigest(t *testing.T) {
	t.Parallel()

	source, err := Normalize("api@" + testDigest)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	other, err := domain.ParseDigest("sha256:" + strings.Repeat("f", 64))
	if err != nil {
		t.Fatalf("ParseDigest(other) error = %v", err)
	}

	_, err = source.Pin(other)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Pin(other) error = %v, want ErrInvalid", err)
	}

	zero := Source{value: "", registry: "", digest: domain.Digest{}, pinned: false}

	_, err = zero.Pin(other)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero Source Pin() error = %v, want ErrInvalid", err)
	}

	unpinned, err := Normalize("api")
	if err != nil {
		t.Fatalf("Normalize(api) error = %v", err)
	}

	zeroDigestReference, err := unpinned.Pin(domain.Digest{})
	if err != nil {
		t.Fatalf("Pin(zero Digest) error = %v", err)
	}

	if zeroDigestReference.Digest() != (domain.Digest{}) {
		t.Fatalf("Pin(zero Digest) digest = %v", zeroDigestReference.Digest())
	}
}

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

func TestNormalizeRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"",
		"https://example.com/team/api:1",
		"ocidir://example.com/team/api:1",
		"example.com/team/API:1",
		"example.com/team/api@sha256:" + strings.Repeat("A", 64),
		"example.com/team/api@sha512:" + strings.Repeat("0", 128),
	} {
		_, err := Normalize(value)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("Normalize(%q) error = %v, want ErrInvalid", value, err)
		}
	}
}
