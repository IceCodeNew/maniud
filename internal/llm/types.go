// Package llm owns the bounded provider protocol and recommendation validator.
package llm

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maximumAPIKeyBytes   = 4096
	maximumModelBytes    = 256
	maximumEndpointBytes = 2048
)

// Provider identifies one supported provider route.
type Provider string

const (
	// ProviderDeepSeek uses the dedicated DeepSeek adapter and official endpoint.
	ProviderDeepSeek Provider = "deepseek"
	// ProviderOpenAI uses the official OpenAI endpoint.
	ProviderOpenAI Provider = "openai"
	// ProviderOpenAICompatible uses the OpenAI adapter with a custom HTTPS endpoint.
	ProviderOpenAICompatible Provider = "openai-compatible"
)

// Providers returns supported providers in TUI display order.
func Providers() []Provider {
	return []Provider{ProviderOpenAI, ProviderOpenAICompatible, ProviderDeepSeek}
}

// Config is one fully resolved provider configuration. APIKey must never enter
// logs, presentation values, or persistence outside the protected XDG file.
type Config struct {
	Provider Provider
	Model    string
	Endpoint string
	Timeout  time.Duration
	APIKey   string
}

// Validate checks all request-side field budgets and endpoint policy.
func (config Config) Validate() error {
	if !validProvider(config.Provider) || !validModel(config.Model) ||
		!validAPIKey(config.APIKey) || config.Timeout < 5*time.Second || config.Timeout > 120*time.Second ||
		config.Timeout%time.Second != 0 {
		return &ActionError{Code: ErrorConfigInvalid}
	}
	if config.Provider != ProviderOpenAICompatible {
		if config.Endpoint != "" {
			return &ActionError{Code: ErrorConfigInvalid}
		}

		return nil
	}
	_, err := compatibleEndpoint(config.Endpoint)
	if err != nil {
		return &ActionError{Code: ErrorConfigInvalid}
	}

	return nil
}

// Origin returns the canonical provider origin shown before networking.
func (config Config) Origin() string {
	switch config.Provider {
	case ProviderDeepSeek:
		return "https://api.deepseek.com"
	case ProviderOpenAI:
		return "https://api.openai.com"
	case ProviderOpenAICompatible:
		endpoint, err := compatibleEndpoint(config.Endpoint)
		if err != nil {
			return ""
		}

		return endpoint.Scheme + "://" + endpoint.Host
	default:
		return ""
	}
}

// Identity returns an in-memory digest that invalidates network confirmation
// when any effective provider setting or key changes.
func (config Config) Identity(keySource string) string {
	keyDigest := sha256.Sum256([]byte(config.APIKey))
	payload := strings.Join([]string{
		string(config.Provider), config.Model, config.Endpoint,
		strconv.FormatInt(int64(config.Timeout), 10), keySource, hex.EncodeToString(keyDigest[:]),
	}, "\x00")

	return fmt.Sprintf("%x", sha256.Sum256([]byte(payload)))
}

// ParseTimeout validates the TUI's whole-second timeout value.
func ParseTimeout(value string) (time.Duration, error) {
	seconds, err := strconv.ParseUint(value, 10, 8)
	if err != nil || seconds < 5 || seconds > 120 {
		return 0, &ActionError{Code: ErrorConfigInvalid}
	}

	return time.Duration(seconds) * time.Second, nil
}

// ValidAPIKey reports whether a newly entered key satisfies the request-side
// byte and terminal-text limits. Callers must not include the key in errors.
func ValidAPIKey(key string) bool {
	return validAPIKey(key)
}

// CanonicalQuestion validates and normalizes one free-text user turn.
func CanonicalQuestion(question string) (string, error) {
	return canonicalQuestion(question)
}

// Change is one locally validated deployment field mutation.
type Change struct {
	FieldID string
	Value   string
	Unset   bool
}

// Recommendation is one provider choice that passed JSON, schema, citation,
// terminal-text, and typed deployment-value validation.
type Recommendation struct {
	Index   int
	Summary string
	Changes []Change
}

// Result is one normalized logical completion.
type Result struct {
	RequestedModel string
	ReportedModel  string
	ModelWarning   bool
	Choices        []Recommendation
	identity       string
}

// ErrorCode is a stable, privacy-safe LLM action outcome.
type ErrorCode string

//nolint:revive // Stable code names are self-describing and documented as one protocol set.
const (
	ErrorConfigInvalid    ErrorCode = "llm_config_invalid"
	ErrorQuestionInvalid  ErrorCode = "llm_question_invalid"
	ErrorForbiddenValue   ErrorCode = "llm_forbidden_value"
	ErrorAuthentication   ErrorCode = "llm_authentication_failed"
	ErrorRateLimited      ErrorCode = "llm_rate_limited"
	ErrorContextLimit     ErrorCode = "llm_context_limit"
	ErrorRefused          ErrorCode = "llm_refused"
	ErrorEmptyResponse    ErrorCode = "llm_empty_response"
	ErrorTruncated        ErrorCode = "llm_truncated"
	ErrorInvalidResponse  ErrorCode = "llm_invalid_response"
	ErrorModelUnavailable ErrorCode = "llm_model_unavailable"
	ErrorTimeout          ErrorCode = "llm_timeout"
	ErrorCancelled        ErrorCode = "llm_cancelled"
	ErrorProvider         ErrorCode = "llm_provider_failed"
	ErrorContextStale     ErrorCode = "llm_context_stale"
)

// ActionError contains no provider body or dependency error text.
type ActionError struct {
	Code     ErrorCode
	Category string
}

func (err *ActionError) Error() string {
	if err == nil {
		return "LLM action failed"
	}

	if err.Category == "" {
		return string(err.Code)
	}

	return string(err.Code) + ": " + err.Category
}

func validProvider(provider Provider) bool {
	switch provider {
	case ProviderDeepSeek, ProviderOpenAI, ProviderOpenAICompatible:
		return true
	default:
		return false
	}
}

func validModel(model string) bool {
	return model != "" && len(model) <= maximumModelBytes && strings.TrimSpace(model) == model &&
		!strings.ContainsAny(model, "\x00\r\n")
}

func validAPIKey(key string) bool {
	if key == "" || len(key) > maximumAPIKeyBytes {
		return false
	}
	for _, character := range []byte(key) {
		if character < 0x21 || character > 0x7e {
			return false
		}
	}

	return true
}

func compatibleEndpoint(value string) (*url.URL, error) {
	if !validEndpointText(value) {
		return nil, &ActionError{Code: ErrorConfigInvalid}
	}
	parsed, err := url.Parse(value)
	if err != nil || !validEndpointURL(parsed) {
		return nil, &ActionError{Code: ErrorConfigInvalid}
	}

	return parsed, nil
}

func validEndpointText(value string) bool {
	return value != "" && len(value) <= maximumEndpointBytes && strings.TrimSpace(value) == value
}

func validEndpointURL(parsed *url.URL) bool {
	return parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == "" && parsed.Opaque == ""
}
