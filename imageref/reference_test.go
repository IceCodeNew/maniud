package imageref_test

import (
	_ "crypto/sha512"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"

	"github.com/IceCodeNew/maniud/imageref"
)

const (
	testDigest         = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testDockerRegistry = "docker.io"
	testDockerLatest   = "docker.io/library/api:latest"
	testLatestTag      = "latest"
	testLibraryAPI     = "library/api"
)

func TestNormalizeDockerCompatibleInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input      string
		want       string
		registry   string
		repository string
		tag        string
		pinned     bool
	}{
		{"api", testDockerLatest, testDockerRegistry, testLibraryAPI, testLatestTag, false},
		{testLibraryAPI, testDockerLatest, testDockerRegistry, testLibraryAPI, testLatestTag, false},
		{"docker.io/api:1", "docker.io/library/api:1", testDockerRegistry, testLibraryAPI, "1", false},
		{"index.docker.io/api", testDockerLatest, testDockerRegistry, testLibraryAPI, testLatestTag, false},
		{"registry-1.docker.io/api", testDockerLatest, testDockerRegistry, testLibraryAPI, testLatestTag, false},
		{"EXAMPLE.com/team/api:1", "example.com/team/api:1", "example.com", "team/api", "1", false},
		{"localhost:5000/team/api", "localhost:5000/team/api:latest", "localhost:5000", "team/api", testLatestTag, false},
		{"api@" + testDigest, "docker.io/library/api@" + testDigest, testDockerRegistry, testLibraryAPI, "", true},
		{"api:1@" + testDigest, "docker.io/library/api:1@" + testDigest, testDockerRegistry, testLibraryAPI, "1", true},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			t.Parallel()

			source, err := imageref.Normalize(test.input)
			if err != nil {
				t.Fatalf("Normalize(%q) error = %v", test.input, err)
			}
			got := []any{
				source.String(), source.Registry(), source.Repository(), source.Tag(), source.IsPinned(),
			}
			want := []any{test.want, test.registry, test.repository, test.tag, test.pinned}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Normalize(%q) = %v, want %v", test.input, got, want)
			}
		})
	}
}

func TestSourcePinPreservesTagAndCreatesDigestReference(t *testing.T) {
	t.Parallel()

	source, err := imageref.Normalize("api:1")
	if err != nil {
		t.Fatal(err)
	}
	reference, err := source.Pin(digest.Digest(testDigest))
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}
	got := []any{
		reference.String(), reference.DigestReference(), reference.Registry(),
		reference.Repository(), reference.Tag(), reference.Digest(),
	}
	want := []any{
		"docker.io/library/api:1@" + testDigest, "docker.io/library/api@" + testDigest,
		testDockerRegistry, testLibraryAPI, "1", digest.Digest(testDigest),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Pin() = %v, want %v", got, want)
	}

	pinned, err := imageref.Normalize(reference.String())
	if err != nil {
		t.Fatal(err)
	}
	if same, sameErr := pinned.Pin(digest.Digest(testDigest)); sameErr != nil || same != reference {
		t.Fatalf("Pin(same digest) = %#v, %v", same, sameErr)
	}
	if _, err = pinned.Pin(digest.Digest("sha256:" + strings.Repeat("f", 64))); !errors.Is(err, imageref.ErrInvalid) {
		t.Fatalf("Pin(conflicting digest) error = %v", err)
	}
	if _, err = source.Pin(digest.Digest("invalid")); !errors.Is(err, imageref.ErrInvalid) {
		t.Fatalf("Pin(malformed digest) error = %v", err)
	}
}

func TestParseRequiresCanonicalPinnedReference(t *testing.T) {
	t.Parallel()

	value := "example.com/team/api:1@" + testDigest
	reference, err := imageref.Parse(value)
	if err != nil || reference.String() != value ||
		reference.DigestReference() != "example.com/team/api@"+testDigest {
		t.Fatalf("Parse(%q) = %#v, %v", value, reference, err)
	}

	for _, invalid := range []string{
		"", "api@" + testDigest, "EXAMPLE.com/team/api@" + testDigest,
		"example.com/team/API@" + testDigest, "example.com/team/api:latest",
		"example.com/team/api@sha256:" + strings.Repeat("A", 64),
		"example.com/team/api@sha512:" + strings.Repeat("0", 128),
	} {
		if _, err = imageref.Parse(invalid); !errors.Is(err, imageref.ErrInvalid) {
			t.Errorf("Parse(%q) error = %v", invalid, err)
		}
	}
}

func TestNormalizeRejectsInvalidOrUnsupportedInput(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"", " api", "api ", "https://example.com/team/api:1", "ocidir://example.com/team/api:1",
		"TEAM/api:1", "example.com/team/API:1", "example.com/team/api@@" + testDigest,
		"example.com/team/api@sha256:" + strings.Repeat("A", 64),
		"example.com/team/api@sha512:" + strings.Repeat("0", 128),
	} {
		if _, err := imageref.Normalize(value); !errors.Is(err, imageref.ErrInvalid) {
			t.Errorf("Normalize(%q) error = %v", value, err)
		}
	}
}

func TestZeroValuesFailClosed(t *testing.T) {
	t.Parallel()

	var source imageref.Source
	gotSource := []any{source.String(), source.Registry(), source.Repository(), source.Tag(), source.IsPinned()}
	wantSource := []any{"", "", "", "", false}
	if !reflect.DeepEqual(gotSource, wantSource) {
		t.Fatalf("zero Source = %v, want %v", gotSource, wantSource)
	}
	if _, err := source.Pin(digest.Digest(testDigest)); !errors.Is(err, imageref.ErrInvalid) {
		t.Fatalf("zero Source Pin() error = %v", err)
	}

	var reference imageref.Reference
	gotReference := []any{
		reference.String(), reference.DigestReference(), reference.Registry(),
		reference.Repository(), reference.Tag(), reference.Digest(),
	}
	wantReference := []any{"", "", "", "", "", digest.Digest("")}
	if !reflect.DeepEqual(gotReference, wantReference) {
		t.Fatalf("zero Reference = %v, want %v", gotReference, wantReference)
	}
}
