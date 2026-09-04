package llm

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	anyllm "github.com/mozilla-ai/any-llm-go"
	"github.com/mozilla-ai/any-llm-go/providers/deepseek"
	"github.com/mozilla-ai/any-llm-go/providers/openai"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	maximumTurns                 = 8
	maximumUserTextBytes         = 8 << 10
	maximumHistoryBytes          = 32 << 10
	maximumAssistantBytes        = 32 << 10
	maximumProtocolChoices       = 3
	maximumRecommendationChanges = 14
	completionOutputTokens       = 8192
	completionDeadlineMultiplier = 3
	maximumRedirects             = 10
	maximumQuestionLines         = 64
	maximumMessageBytes          = 1024
	maximumMessageLines          = 8
	messageFixedCapacity         = 2
	openAIBaseURL                = "https://api.openai.com/v1"
	deepSeekBaseURL              = "https://api.deepseek.com"
	jsonSchemaType               = "type"
	jsonSchemaObject             = "object"
	jsonSchemaString             = "string"
)

func responseSchema() map[string]any {
	return map[string]any{
		jsonSchemaType:         jsonSchemaObject,
		"additionalProperties": false,
		"required":             []any{"kind", "message", "changes"},
		"properties": map[string]any{
			"kind": map[string]any{
				jsonSchemaType: jsonSchemaString,
				"enum": []any{
					string(ChoiceAnswer), string(ChoiceClarification), string(ChoiceRecommendation),
				},
			},
			"message": map[string]any{
				jsonSchemaType: jsonSchemaString, "minLength": 1, "maxLength": maximumMessageBytes,
			},
			"changes": map[string]any{
				jsonSchemaType: "array", "minItems": 0, "maxItems": maximumRecommendationChanges,
				"items": map[string]any{
					jsonSchemaType: jsonSchemaObject, "additionalProperties": false,
					"required": []any{"field", "value", "unset", "citation"},
					"properties": map[string]any{
						"field":    map[string]any{jsonSchemaType: jsonSchemaString},
						"value":    map[string]any{jsonSchemaType: jsonSchemaString},
						"unset":    map[string]any{jsonSchemaType: "boolean"},
						"citation": map[string]any{jsonSchemaType: jsonSchemaString},
					},
				},
			},
		},
	}
}

type turn struct {
	question string
	answer   string
}

type wireChoice struct {
	Kind    *string       `json:"kind"`
	Message *string       `json:"message"`
	Changes *[]wireChange `json:"changes"`
}

type wireChange struct {
	Field    *string `json:"field"`
	Value    *string `json:"value"`
	Unset    *bool   `json:"unset"`
	Citation *string `json:"citation"`
}

// Session reuses one provider client for a bounded eight-turn conversation.
type Session struct {
	mu           sync.Mutex
	config       Config
	provider     anyllm.Provider
	httpClient   *http.Client
	transport    *requestTrackingTransport
	turns        []turn
	userBytes    int
	historyBytes int
	pending      *pendingResult
}

type pendingResult struct {
	token    string
	question string
	answers  []string
}

type providerFactory func(Provider, []anyllm.Option) (anyllm.Provider, error)

type requestTrackingTransport struct {
	next     http.RoundTripper
	started  atomic.Bool
	response atomic.Bool
}

func (transport *requestTrackingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.started.Store(true)
	transport.response.Store(false)

	response, err := transport.next.RoundTrip(request)
	transport.response.Store(response != nil && err == nil)
	if err != nil {
		return response, fmt.Errorf("send provider request: %w", err)
	}

	return response, nil
}

func (transport *requestTrackingTransport) CloseIdleConnections() {
	if closer, valid := transport.next.(interface{ CloseIdleConnections() }); valid {
		closer.CloseIdleConnections()
	}
}

func (transport *requestTrackingTransport) reset() {
	transport.started.Store(false)
	transport.response.Store(false)
}

func (transport *requestTrackingTransport) outcome() RequestOutcome {
	switch {
	case !transport.started.Load():
		return RequestNotStarted
	case transport.response.Load():
		return RequestResponseReceived
	default:
		return RequestOutcomeUnknown
	}
}

// NewSession constructs a provider only after the effective configuration is complete.
func NewSession(config Config) (*Session, error) {
	return newSession(config, nil)
}

func newSession(config Config, injected http.RoundTripper) (*Session, error) {
	return newSessionWithProvider(config, injected, newProvider)
}

func newSessionWithProvider(
	config Config,
	injected http.RoundTripper,
	createProvider providerFactory,
) (*Session, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	transport := injected
	if transport == nil {
		defaultHTTPTransport, valid := http.DefaultTransport.(*http.Transport)
		if !valid {
			return nil, &ActionError{Code: ErrorConfigInvalid}
		}
		defaultTransport := defaultHTTPTransport.Clone()
		if defaultTransport.TLSClientConfig == nil {
			defaultTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		} else {
			defaultTransport.TLSClientConfig = defaultTransport.TLSClientConfig.Clone()
			defaultTransport.TLSClientConfig.MinVersion = max(defaultTransport.TLSClientConfig.MinVersion, tls.VersionTLS12)
		}
		transport = defaultTransport
	}
	trackedTransport := &requestTrackingTransport{next: transport}
	client := &http.Client{
		Transport:     trackedTransport,
		Timeout:       config.Timeout,
		CheckRedirect: sameOriginRedirect,
	}
	baseURLs := map[Provider]string{
		ProviderOpenAI:           openAIBaseURL,
		ProviderDeepSeek:         deepSeekBaseURL,
		ProviderOpenAICompatible: config.Endpoint,
	}
	options := []anyllm.Option{
		anyllm.WithAPIKey(config.APIKey),
		anyllm.WithHTTPClient(client),
		anyllm.WithBaseURL(baseURLs[config.Provider]),
	}
	provider, err := createProvider(config.Provider, options)
	if err != nil {
		client.CloseIdleConnections()

		return nil, classifyError(err)
	}

	return &Session{config: config, provider: provider, httpClient: client, transport: trackedTransport}, nil
}

//nolint:ireturn // Session stores the provider interface so dedicated and compatible adapters share one protocol.
func newProvider(provider Provider, options []anyllm.Option) (anyllm.Provider, error) {
	switch provider {
	case ProviderDeepSeek:
		result, err := deepseek.New(options...)
		if err != nil {
			return nil, fmt.Errorf("create DeepSeek provider: %w", err)
		}

		return result, nil
	case ProviderOpenAI, ProviderOpenAICompatible:
		result, err := openai.New(options...)
		if err != nil {
			return nil, fmt.Errorf("create OpenAI provider: %w", err)
		}

		return result, nil
	default:
		return nil, &ActionError{Code: ErrorConfigInvalid}
	}
}

func sameOriginRedirect(request *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	origin := via[0].URL
	if request.URL.Scheme != origin.Scheme || request.URL.Host != origin.Host {
		return http.ErrUseLastResponse
	}
	if len(via) >= maximumRedirects {
		return http.ErrUseLastResponse
	}

	return nil
}

// Recommend performs one non-streaming logical completion. It does not add a
// provider answer to history until Accept is called for the user's choice.
func (session *Session) Recommend(
	ctx context.Context,
	projection application.AssistProjection,
	question string,
) (Result, error) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.provider == nil || projection.Identity == "" {
		return Result{}, requestActionError(
			&ActionError{Code: ErrorConfigInvalid}, ActionStageRequestPreparation, RequestNotStarted,
		)
	}
	canonicalQuestion, err := canonicalQuestion(question)
	if err != nil {
		return Result{}, requestActionError(err, ActionStageRequestPreparation, RequestNotStarted)
	}
	if strings.Contains(canonicalQuestion, session.config.APIKey) {
		return Result{}, requestActionError(
			&ActionError{Code: ErrorForbiddenValue}, ActionStageRequestPreparation, RequestNotStarted,
		)
	}
	if len(session.turns) >= maximumTurns || session.userBytes+len(canonicalQuestion) > maximumUserTextBytes {
		return Result{}, requestActionError(
			&ActionError{Code: ErrorConversationLimit}, ActionStageRequestPreparation, RequestNotStarted,
		)
	}
	projectionJSON, _ := json.Marshal(&projection, json.Deterministic(true))
	messages := session.messages(string(projectionJSON), canonicalQuestion)
	strict := true
	n := 1
	maxTokens := completionOutputTokens
	params := anyllm.CompletionParams{
		Model: session.config.Model, Messages: messages, N: &n, MaxTokens: &maxTokens,
		ReasoningEffort: anyllm.ReasoningEffortNone,
		ResponseFormat: &anyllm.ResponseFormat{Type: "json_schema", JSONSchema: &anyllm.JSONSchema{
			Name: "maniud_deployment_response", Description: "One answer, clarifying question, or cited recommendation",
			Schema: responseSchema(), Strict: &strict,
		}},
	}
	actionCtx, cancel := context.WithTimeout(ctx, completionDeadlineMultiplier*session.config.Timeout)
	defer cancel()
	session.transport.reset()
	response, err := session.provider.Completion(actionCtx, params)
	if err != nil {
		return Result{}, requestActionError(
			classifyError(err), ActionStageProviderRequest, session.transport.outcome(),
		)
	}
	result, answers, err := validateResponse(response, session.config.Model, projection)
	if err != nil {
		return Result{}, requestActionError(
			err, ActionStageProviderResponse, RequestResponseReceived,
		)
	}
	result.Token = rand.Text()
	session.pending = &pendingResult{token: result.Token, question: canonicalQuestion, answers: answers}

	return result, nil
}

// Accept records only the provider choice explicitly selected by the user.
func (session *Session) Accept(token string, choice int) error {
	session.mu.Lock()
	defer session.mu.Unlock()
	pending := session.pending
	if pending == nil || token == "" || token != pending.token || choice < 0 || choice >= len(pending.answers) {
		return &ActionError{Code: ErrorInvalidResponse}
	}
	answer := pending.answers[choice]
	if session.historyBytes+len(pending.question)+len(answer) > maximumHistoryBytes {
		return &ActionError{Code: ErrorConversationLimit}
	}
	session.turns = append(session.turns, turn{question: pending.question, answer: answer})
	session.userBytes += len(pending.question)
	session.historyBytes += len(pending.question) + len(answer)
	session.pending = nil

	return nil
}

// Close releases the provider and idle transport references.
func (session *Session) Close() {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.httpClient != nil {
		session.httpClient.CloseIdleConnections()
	}
	session.provider = nil
	session.httpClient = nil
	session.transport = nil
	session.pending = nil
	clear(session.turns)
	session.turns = nil
	session.config.APIKey = ""
}

func (session *Session) messages(projection string, question string) []anyllm.Message {
	messages := make([]anyllm.Message, 0, messageFixedCapacity+messageFixedCapacity*len(session.turns))
	messages = append(messages, anyllm.Message{
		Role: anyllm.RoleSystem,
		Content: "Use one response kind: answer, clarifying_question, or recommendation. " +
			"Answers and clarifying questions must have no changes. Recommendations may use only portable " +
			"deployment fields in the supplied JSON projection, and every change must cite its exact field ID. " +
			"Return one JSON object and no prose.",
	})
	for _, previous := range session.turns {
		messages = append(messages,
			anyllm.Message{Role: anyllm.RoleUser, Content: previous.question},
			anyllm.Message{Role: anyllm.RoleAssistant, Content: previous.answer},
		)
	}
	messages = append(messages, anyllm.Message{
		Role:    anyllm.RoleUser,
		Content: "Deployment projection:\n" + projection + "\n\nUser request:\n" + question,
	})

	return messages
}

func canonicalQuestion(question string) (string, error) {
	canonical, err := terminaltext.Canonicalize(question, terminaltext.Limits{
		Bytes: maximumUserTextBytes, Runes: maximumUserTextBytes,
		Lines: maximumQuestionLines, LineCells: maximumUserTextBytes,
	})
	if err != nil || strings.TrimSpace(canonical) == "" {
		return "", &ActionError{Code: ErrorQuestionInvalid}
	}

	return canonical, nil
}

//nolint:cyclop // Choice ordering and partial-choice validation are one normalized response boundary.
func validateResponse(
	response *anyllm.ChatCompletion,
	requestedModel string,
	projection application.AssistProjection,
) (Result, []string, error) {
	if response == nil || len(response.Choices) == 0 {
		return Result{}, nil, &ActionError{Code: ErrorEmptyResponse}
	}
	if len(response.Choices) > maximumProtocolChoices {
		return Result{}, nil, &ActionError{Code: ErrorInvalidResponse}
	}
	choices := slices.Clone(response.Choices)
	slices.SortFunc(choices, func(left, right anyllm.Choice) int { return left.Index - right.Index })
	previousIndex := -1
	for _, choice := range choices {
		if choice.Index < 0 || choice.Index <= previousIndex {
			return Result{}, nil, &ActionError{Code: ErrorInvalidResponse}
		}
		previousIndex = choice.Index
	}
	result := Result{
		RequestedModel: requestedModel, ReportedModel: response.Model,
		ModelWarning: response.Model == "" || response.Model != requestedModel,
		Choices:      make([]Choice, 0, len(choices)),
	}
	answers := make([]string, 0, len(choices))
	var choiceErr error
	for _, choice := range choices {
		recommendation, canonical, err := validateChoice(choice, projection)
		if err != nil {
			if choiceErr == nil {
				choiceErr = err
			}

			continue
		}
		result.Choices = append(result.Choices, recommendation)
		answers = append(answers, canonical)
	}
	if len(result.Choices) == 0 {
		return Result{}, nil, choiceErr
	}

	return result, answers, nil
}

//nolint:cyclop,funlen // Finish state, envelope, schema, and typed patch validation fail closed together.
func validateChoice(
	choice anyllm.Choice,
	projection application.AssistProjection,
) (Choice, string, error) {
	switch choice.FinishReason {
	case anyllm.FinishReasonContentFilter:
		return Choice{}, "", &ActionError{Code: ErrorRefused}
	case anyllm.FinishReasonLength:
		return Choice{}, "", &ActionError{Code: ErrorTruncated}
	case anyllm.FinishReasonStop:
	default:
		return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
	}
	if len(choice.Message.ToolCalls) != 0 {
		return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
	}
	content, valid := choice.Message.Content.(string)
	if !valid || content == "" {
		return Choice{}, "", &ActionError{Code: ErrorEmptyResponse}
	}
	if len(content) > maximumAssistantBytes {
		return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
	}
	var wire wireChoice
	if err := json.Unmarshal([]byte(content), &wire, json.RejectUnknownMembers(true)); err != nil {
		return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
	}
	if wire.Kind == nil || wire.Message == nil || wire.Changes == nil {
		return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
	}
	message, err := terminaltext.Canonicalize(*wire.Message, terminaltext.Limits{
		Bytes: maximumMessageBytes, Runes: maximumMessageBytes,
		Lines: maximumMessageLines, LineCells: maximumMessageBytes,
	})
	kind := ChoiceKind(*wire.Kind)
	if err != nil || strings.TrimSpace(message) == "" ||
		!validChoiceShape(kind, len(*wire.Changes)) {
		return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
	}
	available := make(map[string]application.DeploymentFieldState, len(projection.Fields))
	for _, field := range projection.Fields {
		available[field.ID] = field
	}
	seen := make(map[string]struct{}, len(*wire.Changes))
	changes := make([]Change, 0, len(*wire.Changes))
	for _, change := range *wire.Changes {
		if change.Field == nil || change.Value == nil || change.Unset == nil || change.Citation == nil {
			return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
		}
		field, found := available[*change.Field]
		_, duplicate := seen[*change.Field]
		if !found || duplicate || !field.Available || *change.Citation != *change.Field ||
			(*change.Unset && !field.AllowsUnset) {
			return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
		}
		if _, err = application.ParseDeploymentPatch(*change.Field, *change.Value, *change.Unset); err != nil {
			return Choice{}, "", &ActionError{Code: ErrorInvalidResponse}
		}
		seen[*change.Field] = struct{}{}
		changes = append(changes, Change{FieldID: *change.Field, Value: *change.Value, Unset: *change.Unset})
	}
	canonical, _ := json.Marshal(&wire, json.Deterministic(true))

	return Choice{Kind: kind, Message: message, Changes: changes}, string(canonical), nil
}

func validChoiceShape(kind ChoiceKind, changes int) bool {
	switch kind {
	case ChoiceAnswer, ChoiceClarification:
		return changes == 0
	case ChoiceRecommendation:
		return changes > 0 && changes <= maximumRecommendationChanges
	default:
		return false
	}
}

func classifyError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return &ActionError{Code: ErrorCancelled}
	case errors.Is(err, context.DeadlineExceeded):
		return &ActionError{Code: ErrorTimeout}
	case errors.Is(err, anyllm.ErrAuthentication), errors.Is(err, anyllm.ErrMissingAPIKey):
		return &ActionError{Code: ErrorAuthentication}
	case errors.Is(err, anyllm.ErrRateLimit), errors.Is(err, anyllm.ErrInsufficientFunds):
		return &ActionError{Code: ErrorRateLimited}
	case errors.Is(err, anyllm.ErrContextLength):
		return &ActionError{Code: ErrorContextLimit}
	case errors.Is(err, anyllm.ErrContentFilter):
		return &ActionError{Code: ErrorRefused}
	case errors.Is(err, anyllm.ErrModelNotFound):
		return &ActionError{Code: ErrorModelUnavailable}
	default:
		return &ActionError{Code: ErrorProvider}
	}
}

func requestActionError(err error, stage ActionStage, outcome RequestOutcome) error {
	action, valid := errors.AsType[*ActionError](err)
	if !valid {
		action = &ActionError{Code: ErrorProvider}
	}

	return &ActionError{
		Code: action.Code, Category: action.Category, Stage: stage, RequestOutcome: outcome,
	}
}
