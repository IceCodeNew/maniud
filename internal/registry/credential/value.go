// Package credential defines the ephemeral secret passed from registry
// authentication to runtime adapters.
package credential

import (
	"context"

	"github.com/IceCodeNew/maniud/internal/imageref"
)

// Value contains credentials for one registry. Callers must not persist or log
// this value.
type Value struct {
	Username     string
	Password     string
	RefreshToken string
	AccessToken  string
}

// Provider returns one ephemeral credential for a canonical image reference.
// Implementations must not persist or log the returned value.
type Provider interface {
	Credentials(ctx context.Context, reference imageref.Reference) (Value, error)
}
