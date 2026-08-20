package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// ErrInvalidDigest reports a value outside the canonical SHA-256 form.
var ErrInvalidDigest = errors.New("invalid SHA-256 digest")

// Digest is one SHA-256 identity stored without its source bytes.
type Digest [sha256.Size]byte

// Hash returns the SHA-256 identity of value.
func Hash(value []byte) Digest {
	return sha256.Sum256(value)
}

// ParseDigest parses an algorithm-qualified lowercase SHA-256 digest.
func ParseDigest(value string) (Digest, error) {
	const prefix = "sha256:"

	var digest Digest

	encoded, found := strings.CutPrefix(value, prefix)
	if !found || len(encoded) != hex.EncodedLen(len(digest)) || encoded != strings.ToLower(encoded) {
		return digest, ErrInvalidDigest
	}

	decoded, err := hex.DecodeString(encoded)
	if err != nil {
		return digest, ErrInvalidDigest
	}

	copy(digest[:], decoded)

	return digest, nil
}

// String returns the algorithm-qualified lowercase digest.
func (digest Digest) String() string {
	return "sha256:" + hex.EncodeToString(digest[:])
}
