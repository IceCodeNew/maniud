package llm

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

const testOpenAIOrigin = "https://api.openai.com"

var errLLMCoverageFixture = errors.New("LLM coverage fixture failure")

func TestProviderConfigurationBoundaries(t *testing.T) {
	t.Parallel()
	if !slices.Equal(Providers(), []Provider{ProviderOpenAI, ProviderOpenAICompatible, ProviderDeepSeek}) {
		t.Fatalf("Providers() = %q", Providers())
	}
	for _, test := range []struct {
		name   string
		config Config
		valid  bool
		origin string
	}{
		{name: "openai", config: testConfig(ProviderOpenAI, ""), valid: true, origin: testOpenAIOrigin},
		{name: "deepseek", config: testConfig(ProviderDeepSeek, ""), valid: true, origin: "https://api.deepseek.com"},
		{name: "compatible", config: testConfig(ProviderOpenAICompatible, "https://example.com/v1"),
			valid: true, origin: "https://example.com"},
		{name: "unknown provider", config: testConfig(Provider("unknown"), "")},
		{name: "official endpoint override", config: testConfig(ProviderOpenAI, "https://example.com"),
			origin: testOpenAIOrigin},
		{name: "compatible missing endpoint", config: testConfig(ProviderOpenAICompatible, "")},
		{name: "short timeout", config: Config{Provider: ProviderOpenAI, Model: testModel,
			Timeout: time.Second, APIKey: testAPIKey}, origin: testOpenAIOrigin},
		{name: "fractional timeout", config: Config{Provider: ProviderOpenAI, Model: testModel,
			Timeout: 5500 * time.Millisecond, APIKey: testAPIKey}, origin: testOpenAIOrigin},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := test.config.Validate()
			if (err == nil) != test.valid || test.config.Origin() != test.origin {
				t.Fatalf("Validate() = %v, Origin() = %q", err, test.config.Origin())
			}
		})
	}
}

func TestConfigIdentityTracksEffectiveSecretAndSource(t *testing.T) {
	t.Parallel()
	config := testConfig(ProviderOpenAI, "")
	identity := config.Identity("current .env")
	if len(identity) != sha256.Size*2 || identity != config.Identity("current .env") ||
		strings.Contains(identity, config.APIKey) {
		t.Fatalf("Identity() = %q", identity)
	}
	changedKey := config
	changedKey.APIKey += "-changed"
	if identity == changedKey.Identity("current .env") || identity == config.Identity("process environment") {
		t.Fatal("Identity() did not track effective key or source")
	}
}

//nolint:cyclop // Every endpoint, model, key, and typed-error boundary is asserted independently.
func TestEndpointModelKeyAndErrorBoundaries(t *testing.T) {
	t.Parallel()
	for value, valid := range map[string]bool{"5": true, "invalid": false, "4": false, "121": false} {
		_, err := ParseTimeout(value)
		if (err == nil) != valid {
			t.Fatalf("ParseTimeout(%q) error = %v", value, err)
		}
	}
	for _, endpoint := range []string{
		" http://example.com", "http://example.com", "https://", "https://user@example.com",
		"https://example.com?query=1", "https://example.com#fragment", "mailto:person@example.com",
	} {
		if _, err := compatibleEndpoint(endpoint); err == nil {
			t.Fatalf("compatibleEndpoint(%q) succeeded", endpoint)
		}
	}
	if _, err := compatibleEndpoint("https://[invalid"); err == nil {
		t.Fatal("malformed endpoint succeeded")
	}
	for _, model := range []string{"", " model", "model\nname", strings.Repeat("m", maximumModelBytes+1)} {
		config := testConfig(ProviderOpenAI, "")
		config.Model = model
		if config.Validate() == nil {
			t.Fatalf("model %q succeeded", model)
		}
	}
	for _, key := range []string{"", "key with space", "key\nvalue", strings.Repeat("k", maximumAPIKeyBytes+1)} {
		if ValidAPIKey(key) {
			t.Fatalf("ValidAPIKey(%q) = true", key)
		}
	}
	if !ValidAPIKey("printable-key") {
		t.Fatal("printable key rejected")
	}
	var nilError *ActionError
	if nilError.Error() == "" || (&ActionError{Code: ErrorForbiddenValue, Category: "credential"}).Error() == "" ||
		(&ActionError{Code: ErrorProvider}).Error() == "" {
		t.Fatal("ActionError returned an empty message")
	}
	if _, err := CanonicalQuestion("valid question"); err != nil {
		t.Fatalf("CanonicalQuestion() error = %v", err)
	}
}

//nolint:cyclop,funlen,paralleltest // The test temporarily replaces http.DefaultTransport.
func TestSessionConstructionRedirectAndCloseBoundaries(t *testing.T) {
	original := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = original })
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errLLMCoverageFixture
	})
	if _, err := NewSession(testConfig(ProviderOpenAI, "")); !isActionError(err, ErrorConfigInvalid) {
		t.Fatalf("NewSession(nonstandard transport) error = %v", err)
	}
	http.DefaultTransport = &http.Transport{
		TLSNextProto: make(map[string]func(string, *tls.Conn) http.RoundTripper),
	}
	if _, err := NewSession(Config{}); !isActionError(err, ErrorConfigInvalid) {
		t.Fatalf("NewSession(invalid config) error = %v", err)
	}
	session, err := newSessionWithProvider(
		testConfig(ProviderOpenAI, ""), nil,
		func(Provider, []anyllm.Option) (anyllm.Provider, error) { return &providerFixture{}, nil },
	)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	session.Close()
	session.Close()
	if _, err = newSessionWithProvider(
		testConfig(ProviderOpenAI, ""), nil,
		func(Provider, []anyllm.Option) (anyllm.Provider, error) {
			return nil, errLLMCoverageFixture
		},
	); !isActionError(err, ErrorProvider) {
		t.Fatalf("newSessionWithProvider() error = %v", err)
	}
	http.DefaultTransport = &http.Transport{
		//nolint:gosec // The constructor must raise this fixture's lower minimum to TLS 1.2.
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS10},
	}
	tlsSession, err := NewSession(testConfig(ProviderDeepSeek, ""))
	if err != nil {
		t.Fatalf("NewSession(existing TLS config) error = %v", err)
	}
	tlsSession.Close()
	failingOption := anyllm.Option(func(*anyllm.Config) error { return errLLMCoverageFixture })
	for _, provider := range []Provider{ProviderDeepSeek, ProviderOpenAI} {
		if _, err = newProvider(provider, []anyllm.Option{failingOption}); err == nil {
			t.Fatalf("newProvider(%s, failing option) succeeded", provider)
		}
	}
	if _, err = newProvider(Provider("unknown"), nil); !isActionError(err, ErrorConfigInvalid) {
		t.Fatalf("newProvider() error = %v", err)
	}
	origin, _ := url.Parse("https://example.com/v1")
	request := &http.Request{URL: origin}
	if err = sameOriginRedirect(request, nil); err != nil {
		t.Fatalf("first redirect error = %v", err)
	}
	if err = sameOriginRedirect(request, []*http.Request{{URL: origin}}); err != nil {
		t.Fatalf("same-origin redirect error = %v", err)
	}
	other, _ := url.Parse("https://other.example/v1")
	err = sameOriginRedirect(&http.Request{URL: other}, []*http.Request{{URL: origin}})
	if !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("cross-origin redirect error = %v", err)
	}
	via := make([]*http.Request, maximumRedirects)
	via[0] = &http.Request{URL: origin}
	if err = sameOriginRedirect(request, via); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect limit error = %v", err)
	}
}

//nolint:cyclop // Request budgets, provider failure, and acceptance each require a separate assertion.
func TestSessionRequestAndAcceptBoundaries(t *testing.T) {
	t.Parallel()
	session := &Session{config: testConfig(ProviderOpenAI, ""), pending: make(map[string]pendingResult)}
	if _, err := session.Recommend(t.Context(), testProjection(), testQuestion); !isActionError(err, ErrorConfigInvalid) {
		t.Fatalf("Recommend(nil provider) error = %v", err)
	}
	session.provider = &providerFixture{response: completionChatResponse()}
	session.provider = &providerFixture{err: errLLMCoverageFixture}
	if _, err := session.Recommend(t.Context(), testProjection(), testQuestion); !isActionError(err, ErrorProvider) {
		t.Fatalf("Recommend(provider failure) error = %v", err)
	}
	session.provider = &providerFixture{response: completionChatResponse()}
	for _, question := range []string{"", testAPIKey} {
		_, err := session.Recommend(t.Context(), testProjection(), question)
		if err == nil {
			t.Fatalf("Recommend(%q) succeeded", question)
		}
	}
	projection := testProjection()
	projection.Identity = ""
	if _, err := session.Recommend(t.Context(), projection, testQuestion); !isActionError(err, ErrorConfigInvalid) {
		t.Fatalf("Recommend(empty identity) error = %v", err)
	}
	session.turns = make([]turn, maximumTurns)
	if _, err := session.Recommend(
		t.Context(), testProjection(), testQuestion,
	); !isActionError(err, ErrorQuestionInvalid) {
		t.Fatalf("Recommend(turn limit) error = %v", err)
	}
	session.turns = nil
	session.userBytes = maximumUserTextBytes
	if _, err := session.Recommend(
		t.Context(), testProjection(), testQuestion,
	); !isActionError(err, ErrorQuestionInvalid) {
		t.Fatalf("Recommend(user budget) error = %v", err)
	}
	session.userBytes = 0
	result, err := session.Recommend(t.Context(), testProjection(), testQuestion)
	if err != nil {
		t.Fatalf("Recommend() error = %v", err)
	}
	if err = session.Accept(result, -1); !isActionError(err, ErrorInvalidResponse) {
		t.Fatalf("Accept(invalid choice) error = %v", err)
	}
	session.historyBytes = maximumHistoryBytes
	if err = session.Accept(result, 0); !isActionError(err, ErrorQuestionInvalid) {
		t.Fatalf("Accept(history budget) error = %v", err)
	}
}

func TestResponseValidationAndErrorClassificationBoundaries(t *testing.T) {
	t.Parallel()
	projection := testProjection()
	for _, test := range []struct {
		name     string
		response *anyllm.ChatCompletion
	}{
		{name: "nil"},
		{name: "too many", response: &anyllm.ChatCompletion{Choices: make([]anyllm.Choice, maximumRecommendations+1)}},
		{name: "descending", response: &anyllm.ChatCompletion{Choices: []anyllm.Choice{
			anyLLMTestChoice(completionContent()), anyLLMTestChoice(completionContent()),
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := validateResponse(test.response, testModel, projection); err == nil {
				t.Fatal("validateResponse() succeeded")
			}
		})
	}
	for _, choice := range invalidChoices() {
		if _, _, err := validateChoice(choice, projection); err == nil {
			t.Fatalf("validateChoice(%#v) succeeded", choice)
		}
	}
	for cause, code := range map[error]ErrorCode{
		context.Canceled: ErrorCancelled, context.DeadlineExceeded: ErrorTimeout,
		anyllm.ErrAuthentication: ErrorAuthentication, anyllm.ErrMissingAPIKey: ErrorAuthentication,
		anyllm.ErrRateLimit: ErrorRateLimited, anyllm.ErrInsufficientFunds: ErrorRateLimited,
		anyllm.ErrContextLength: ErrorContextLimit, anyllm.ErrContentFilter: ErrorRefused,
		anyllm.ErrModelNotFound: ErrorModelUnavailable, errLLMCoverageFixture: ErrorProvider,
	} {
		if err := classifyError(cause); !isActionError(err, code) {
			t.Fatalf("classifyError(%v) = %v, want %s", cause, err, code)
		}
	}
}

type providerFixture struct {
	response *anyllm.ChatCompletion
	err      error
}

func (*providerFixture) Name() string {
	return "fixture"
}

func (fixture *providerFixture) Completion(context.Context, anyllm.CompletionParams) (*anyllm.ChatCompletion, error) {
	return fixture.response, fixture.err
}

func (fixture *providerFixture) CompletionStream(
	context.Context,
	anyllm.CompletionParams,
) (<-chan anyllm.ChatCompletionChunk, <-chan error) {
	return nil, nil
}

func completionChatResponse() *anyllm.ChatCompletion {
	return &anyllm.ChatCompletion{Model: testModel, Choices: []anyllm.Choice{anyLLMTestChoice(completionContent())}}
}

func completionContent() string {
	return `{"summary":"Set CPUs","changes":[` +
		`{"field":"cpus","value":"2","unset":false,"citation":"cpus"}]}`
}

func invalidChoices() []anyllm.Choice {
	toolChoice := anyLLMTestChoice(completionContent())
	toolChoice.Message.ToolCalls = []anyllm.ToolCall{{ID: "call"}}
	oversized := anyLLMTestChoice(strings.Repeat("x", maximumAssistantBytes+1))
	missingField := anyLLMTestChoice(`{"summary":"missing field","changes":[` +
		`{"value":"2","unset":false,"citation":"cpus"}]}`)
	unknownField := anyLLMTestChoice(`{"summary":"unknown","changes":[` +
		`{"field":"unknown","value":"2","unset":false,"citation":"unknown"}]}`)
	badCitation := anyLLMTestChoice(`{"summary":"citation","changes":[` +
		`{"field":"cpus","value":"2","unset":false,"citation":"memory"}]}`)
	badValue := anyLLMTestChoice(`{"summary":"value","changes":[` +
		`{"field":"cpus","value":"invalid","unset":false,"citation":"cpus"}]}`)
	duplicate := anyLLMTestChoice(`{"summary":"duplicate","changes":[` +
		`{"field":"cpus","value":"2","unset":false,"citation":"cpus"},` +
		`{"field":"cpus","value":"3","unset":false,"citation":"cpus"}]}`)
	unavailableProjection := testProjection()
	unavailableProjection.Fields[0].Available = false
	_ = unavailableProjection

	return []anyllm.Choice{
		{FinishReason: "unexpected"}, toolChoice,
		{FinishReason: anyllm.FinishReasonStop, Message: anyllm.Message{Content: nil}},
		oversized, anyLLMTestChoice("{"), anyLLMTestChoice(`{"summary":"","changes":[]}`),
		missingField, unknownField, badCitation, badValue, duplicate,
	}
}
