// Package argv establishes a bounded, immutable boundary around untrusted
// command-line arguments before a syntax-specific parser consumes them.
package argv

import (
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	maximumArguments  = 4096
	maximumTotalBytes = 1 << 20
	// MaximumTokenBytes is the largest accepted UTF-8 encoding of one argument.
	MaximumTokenBytes = 4096
)

// ErrInvalid reports command-line arguments that exceed the trust boundary.
var ErrInvalid = errors.New("command-line arguments are invalid")

// Validate checks the size and encoding of untrusted arguments and returns an
// owned copy that a syntax-specific parser can safely retain.
func Validate(arguments []string) ([]string, error) {
	if len(arguments) > maximumArguments {
		return nil, ErrInvalid
	}

	total := 0
	for _, argument := range arguments {
		if !ValidToken(argument) {
			return nil, ErrInvalid
		}
		total += len(argument)
		if total > maximumTotalBytes {
			return nil, ErrInvalid
		}
	}

	return slices.Clone(arguments), nil
}

// ValidToken reports whether one argument has bounded, valid UTF-8 content
// without a NUL byte. Empty arguments are valid and retain their argv meaning.
func ValidToken(value string) bool {
	return len(value) <= MaximumTokenBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
