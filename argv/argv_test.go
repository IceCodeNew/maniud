package argv

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestValidateReturnsOwnedArguments(t *testing.T) {
	t.Parallel()

	arguments := []string{"docker", "run", "image:latest", "--", "internal-cmd", "--help", ""}
	validated, err := Validate(arguments)
	if err != nil || !slices.Equal(validated, arguments) {
		t.Fatalf("Validate() = %q, %v", validated, err)
	}
	arguments[0] = "changed"
	if validated[0] != "docker" {
		t.Fatalf("Validate() retained caller-owned storage: %q", validated)
	}
}

func TestValidateRejectsUntrustedArgumentBounds(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{strings.Repeat("a", MaximumTokenBytes+1)},
		{string([]byte{0xff})},
		{"before\x00after"},
		make([]string, maximumArguments+1),
		make([]string, maximumTotalBytes/MaximumTokenBytes+1),
	}
	for index := range tests[len(tests)-1] {
		tests[len(tests)-1][index] = strings.Repeat("a", MaximumTokenBytes)
	}

	for _, arguments := range tests {
		if validated, err := Validate(arguments); !errors.Is(err, ErrInvalid) || validated != nil {
			t.Fatalf("Validate(%d arguments) = %q, %v", len(arguments), validated, err)
		}
	}
}

func TestValidTokenAcceptsEmptyAndUnicodeArguments(t *testing.T) {
	t.Parallel()

	if !ValidToken("") || !ValidToken("\u5bb9\u5668") {
		t.Fatal("ValidToken() rejected valid argv content")
	}
}
