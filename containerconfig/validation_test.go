package containerconfig_test

import (
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
)

func TestValidationErrorDoesNotExposeValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  containerconfig.ValidationError
		want string
	}{
		{
			name: "complete input",
			err:  containerconfig.ValidationError{Code: containerconfig.ValidationInvalidDocument},
			want: "container configuration validation failed: invalid_document",
		},
		{
			name: "field",
			err: containerconfig.ValidationError{
				Code: containerconfig.ValidationUnsupportedCapability,
				Path: "/services/api/ports/0",
			},
			want: "container configuration validation failed: unsupported_capability at /services/api/ports/0",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := test.err.Error(); got != test.want {
				t.Fatalf("ValidationError.Error() = %q, want %q", got, test.want)
			}
		})
	}
}
