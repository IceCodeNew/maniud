package domain

import (
	"errors"
	"strings"
	"testing"
)

func TestDigest(t *testing.T) {
	t.Parallel()

	const want = "sha256:82159f837b80a2be64c96f964159c7146970e2a09d7c54b4e493a2595baa7dfe"

	digest := Hash([]byte("maniud"))

	if digest.String() != want {
		t.Fatalf("Hash(maniud).String() = %q, want %q", digest.String(), want)
	}

	parsed, err := ParseDigest(want)
	if err != nil {
		t.Fatalf("ParseDigest() error = %v", err)
	}

	if parsed != digest {
		t.Fatalf("ParseDigest() = %s, want %s", parsed, digest)
	}
}

func TestParseDigestRejectsNonCanonicalValue(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"sha512:" + strings.Repeat("0", 64),
		"sha256:" + strings.Repeat("0", 63),
		"sha256:" + strings.Repeat("A", 64),
		"sha256:" + strings.Repeat("z", 64),
	}

	for _, value := range tests {
		_, err := ParseDigest(value)
		if !errors.Is(err, ErrInvalidDigest) {
			t.Fatalf("ParseDigest(%q) error = %v, want ErrInvalidDigest", value, err)
		}
	}
}

func FuzzDigestRoundTrip(f *testing.F) {
	f.Add([]byte("maniud"))

	f.Fuzz(func(t *testing.T, value []byte) {
		digest := Hash(value)

		parsed, err := ParseDigest(digest.String())
		if err != nil {
			t.Fatalf("ParseDigest(Hash(value).String()) error = %v", err)
		}

		if parsed != digest {
			t.Fatalf("ParseDigest(Hash(value).String()) = %s, want %s", parsed, digest)
		}
	})
}
