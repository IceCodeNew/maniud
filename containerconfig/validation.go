package containerconfig

// ValidationCode classifies a portable or adapter-specific configuration
// rejection without exposing the rejected value.
type ValidationCode string

const (
	// ValidationInvalidDocument reports malformed source syntax or structure.
	ValidationInvalidDocument ValidationCode = "invalid_document"
	// ValidationUnsupportedField reports a source field outside the adapter contract.
	ValidationUnsupportedField ValidationCode = "unsupported_field"
	// ValidationInvalidValue reports a supported field with an invalid value.
	ValidationInvalidValue ValidationCode = "invalid_value"
	// ValidationUnsupportedCapability reports valid configuration that the selected
	// runtime cannot implement.
	ValidationUnsupportedCapability ValidationCode = "unsupported_capability"
)

// ValidationError identifies one rejected field without retaining its value.
// Path is an RFC 6901 JSON Pointer. An empty path identifies the complete input.
type ValidationError struct {
	Code ValidationCode
	Path string
}

// Error returns a stable, value-free diagnostic.
func (validation ValidationError) Error() string {
	message := "container configuration validation failed: " + string(validation.Code)
	if validation.Path != "" {
		message += " at " + validation.Path
	}

	return message
}
