// Package credential defines the ephemeral secret passed from registry
// authentication to runtime adapters.
package credential

// Value contains credentials for one registry. Callers must not persist or log
// this value.
type Value struct {
	Username     string
	Password     string
	RefreshToken string
	AccessToken  string
}
