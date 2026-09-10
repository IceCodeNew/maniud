package llm

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	testAPIKey        = "test-api-key"
	testModel         = "test-model"
	testQuestion      = "Recommend a CPU limit"
	contentTypeHeader = "Content-Type"
	jsonContentType   = "application/json"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestPinnedOfficialAdaptersCarryValidatedRecommendationProtocol(t *testing.T) {
	t.Parallel()
	for provider, expectedURL := range map[Provider]string{
		ProviderOpenAI:   openAIBaseURL + "/chat/completions",
		ProviderDeepSeek: deepSeekBaseURL + "/chat/completions",
	} {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			var requestBody []byte
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.String() != expectedURL {
					t.Errorf("request URL = %q, want %q", request.URL, expectedURL)
				}
				var err error
				requestBody, err = io.ReadAll(request.Body)
				if err != nil {
					t.Fatalf("read request: %v", err)
				}

				return completionResponse(request, testModel), nil
			})
			session, err := newSession(testConfig(provider, ""), transport)
			if err != nil {
				t.Fatalf("newSession() error = %v", err)
			}
			t.Cleanup(session.Close)
			result, err := session.Recommend(t.Context(), testProjection(), testQuestion)
			if err != nil {
				t.Fatalf("Recommend() error = %v", err)
			}
			assertRecommendationResult(t, result)
			assertProductRequestContract(t, requestBody, false)
		})
	}
}

func TestPinnedOfficialAdaptersBoundLogicalCompletionToThreeAttempts(t *testing.T) {
	t.Parallel()
	for _, provider := range []Provider{ProviderOpenAI, ProviderDeepSeek} {
		t.Run(string(provider), func(t *testing.T) {
			t.Parallel()
			attempts := 0
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				attempts++

				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header: http.Header{
						contentTypeHeader: []string{jsonContentType},
						"Retry-After-Ms":  []string{"0"},
					},
					Body: io.NopCloser(strings.NewReader(
						`{"error":{"message":"fixture","type":"server_error","code":"server_error"}}`,
					)),
					Request: request,
				}, nil
			})
			session, err := newSession(testConfig(provider, ""), transport)
			if err != nil {
				t.Fatalf("newSession() error = %v", err)
			}
			t.Cleanup(session.Close)
			if _, err = session.Recommend(t.Context(), testProjection(), testQuestion); err == nil {
				t.Fatal("Recommend() succeeded after repeated server failures")
			}
			action, valid := errors.AsType[*ActionError](err)
			if !valid || action.Stage != ActionStageProviderRequest ||
				action.RequestOutcome != RequestResponseReceived {
				t.Fatalf("request failure = %#v", action)
			}
			if attempts != 3 {
				t.Fatalf("attempts = %d, want 3", attempts)
			}
		})
	}
}

//nolint:cyclop // The test exercises the tracker's complete request lifecycle.
func TestRequestTrackingTransportClassifiesLastAttempt(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://provider.example/v1", nil)
	tracker := &requestTrackingTransport{next: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	})}
	if outcome := tracker.outcome(); outcome != RequestNotStarted {
		t.Fatalf("initial outcome = %q", outcome)
	}
	failedResponse, err := tracker.RoundTrip(request)
	if failedResponse != nil {
		if closeErr := failedResponse.Body.Close(); closeErr != nil {
			t.Fatalf("close failed response: %v", closeErr)
		}
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	if outcome := tracker.outcome(); outcome != RequestOutcomeUnknown {
		t.Fatalf("failed attempt outcome = %q", outcome)
	}
	tracker.reset()
	if outcome := tracker.outcome(); outcome != RequestNotStarted {
		t.Fatalf("reset outcome = %q", outcome)
	}
	tracker.next = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return completionResponse(request, testModel), nil
	})
	response, err := tracker.RoundTrip(request)
	if err != nil || response == nil || tracker.outcome() != RequestResponseReceived {
		t.Fatalf("response attempt = %#v, %v, %q", response, err, tracker.outcome())
	}
	t.Cleanup(func() {
		if err := response.Body.Close(); err != nil {
			t.Errorf("close response: %v", err)
		}
	})
	tracker.CloseIdleConnections()
}

func TestPinnedCompatibleAdapterUsesLocalTLSAndOmitsAbsentAssistantToolCalls(t *testing.T) {
	t.Parallel()
	requestBodies := make(chan []byte, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			response.WriteHeader(http.StatusInternalServerError)

			return
		}
		requestBodies <- body
		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response, completionBody(testModel))
	}))
	t.Cleanup(server.Close)
	session, err := newSession(testConfig(ProviderOpenAICompatible, server.URL), server.Client().Transport)
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}
	t.Cleanup(session.Close)
	first, err := session.Recommend(t.Context(), testProjection(), testQuestion)
	if err != nil {
		t.Fatalf("Recommend(first) error = %v", err)
	}
	if err = session.Accept(first.Token, 0); err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	second, err := session.Recommend(t.Context(), testProjection(), "Keep the limit conservative")
	if err != nil {
		t.Fatalf("Recommend(second) error = %v", err)
	}
	assertRecommendationResult(t, second)
	assertProductRequestContract(t, <-requestBodies, false)
	assertProductRequestContract(t, <-requestBodies, true)
}

//nolint:cyclop // One conversation must prove both validated response kinds and their shared history.
func TestSessionAcceptsAnswerAndClarificationTurns(t *testing.T) {
	t.Parallel()
	responses := []string{
		`{"kind":"clarifying_question","message":"How much memory is available?","changes":[]}`,
		`{"kind":"answer","message":"Two CPUs fit the stated workload.","changes":[]}`,
	}
	requestBodies := make([][]byte, 0, len(responses))
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		requestBodies = append(requestBodies, body)
		content := responses[len(requestBodies)-1]

		return completionResponseWithBody(
			request, completionBodyWithChoices(testModel, completionChoiceWithContent(0, "stop", content)),
		), nil
	})
	session, err := newSession(testConfig(ProviderOpenAI, ""), transport)
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}
	t.Cleanup(session.Close)

	clarification, err := session.Recommend(t.Context(), testProjection(), testQuestion)
	if err != nil {
		t.Fatalf("Recommend(clarification) error = %v", err)
	}
	if len(clarification.Choices) != 1 ||
		clarification.Choices[0].Kind != ChoiceClarification ||
		len(clarification.Choices[0].Changes) != 0 {
		t.Fatalf("clarification = %#v", clarification)
	}
	if err = session.Accept(clarification.Token, 0); err != nil {
		t.Fatalf("Accept(clarification) error = %v", err)
	}
	answer, err := session.Recommend(t.Context(), testProjection(), "The host has 4 GiB available")
	if err != nil {
		t.Fatalf("Recommend(answer) error = %v", err)
	}
	if len(answer.Choices) != 1 || answer.Choices[0].Kind != ChoiceAnswer ||
		answer.Choices[0].Message != "Two CPUs fit the stated workload." ||
		len(answer.Choices[0].Changes) != 0 {
		t.Fatalf("answer = %#v", answer)
	}
	if err = session.Accept(answer.Token, 0); err != nil {
		t.Fatalf("Accept(answer) error = %v", err)
	}
	if len(session.turns) != 2 || !bytes.Contains(requestBodies[1], []byte("How much memory is available?")) {
		t.Fatalf("conversation history = %#v; second request = %s", session.turns, requestBodies[1])
	}
}

func TestSessionNormalizesProviderChoiceIndexesBeforeSelection(t *testing.T) {
	t.Parallel()

	lower := `{"kind":"recommendation","message":"Lower provider index","changes":[` +
		`{"field":"cpus","value":"2","unset":false,"citation":"cpus"}]}`
	higher := `{"kind":"recommendation","message":"Higher provider index","changes":[` +
		`{"field":"cpus","value":"3","unset":false,"citation":"cpus"}]}`
	body := completionBodyWithChoices(
		testModel,
		completionChoiceWithContent(7, "stop", higher),
		completionChoiceWithContent(2, "stop", lower),
	)
	session, err := newSession(testConfig(ProviderOpenAI, ""), staticCompletionTransport(body))
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}
	t.Cleanup(session.Close)
	result, err := session.Recommend(t.Context(), testProjection(), testQuestion)
	if err != nil {
		t.Fatalf("Recommend() error = %v", err)
	}
	if len(result.Choices) != 2 || result.Choices[0].Message != "Lower provider index" ||
		result.Choices[1].Message != "Higher provider index" {
		t.Fatalf("normalized choices = %#v", result.Choices)
	}
	if err = session.Accept(result.Token, 0); err != nil {
		t.Fatalf("Accept(normalized first choice) error = %v", err)
	}
	if len(session.turns) != 1 || !strings.Contains(session.turns[0].answer, "Lower provider index") {
		t.Fatalf("accepted turn = %#v", session.turns)
	}
}

func TestSessionRejectsSupersededTokenForIdenticalResults(t *testing.T) {
	t.Parallel()

	session, err := newSession(
		testConfig(ProviderOpenAI, ""), staticCompletionTransport(completionBody(testModel)),
	)
	if err != nil {
		t.Fatalf("newSession() error = %v", err)
	}
	t.Cleanup(session.Close)
	first, err := session.Recommend(t.Context(), testProjection(), testQuestion)
	if err != nil {
		t.Fatalf("Recommend(first) error = %v", err)
	}
	second, err := session.Recommend(t.Context(), testProjection(), testQuestion)
	if err != nil {
		t.Fatalf("Recommend(second) error = %v", err)
	}
	if first.Token == "" || second.Token == "" || first.Token == second.Token {
		t.Fatalf("recommendation tokens = %q, %q", first.Token, second.Token)
	}
	if err = session.Accept(first.Token, 0); !isActionError(err, ErrorInvalidResponse) {
		t.Fatalf("Accept(superseded token) error = %v", err)
	}
	if err = session.Accept(second.Token, 0); err != nil {
		t.Fatalf("Accept(current token) error = %v", err)
	}
}

func TestSessionRejectsProviderChoiceAndFinishBoundaries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		body string
		code ErrorCode
	}{
		{name: "duplicate index", body: completionBodyWithChoices(testModel,
			completionChoice(0, "stop"), completionChoice(0, "stop")), code: ErrorInvalidResponse},
		{name: "negative index", body: completionBodyWithChoices(testModel,
			completionChoice(-1, "stop")), code: ErrorInvalidResponse},
		{name: "truncated", body: completionBodyWithChoices(testModel,
			completionChoice(0, "length")), code: ErrorTruncated},
		{name: "filtered", body: completionBodyWithChoices(testModel,
			completionChoice(0, "content_filter")), code: ErrorRefused},
		{name: "empty", body: completionBodyWithChoices(testModel), code: ErrorEmptyResponse},
		{name: "missing kind", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"message":"missing kind","changes":[]}`)),
			code: ErrorInvalidResponse},
		{name: "missing message", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"kind":"answer","changes":[]}`)),
			code: ErrorInvalidResponse},
		{name: "missing changes", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"kind":"answer","message":"missing changes"}`)),
			code: ErrorInvalidResponse},
		{name: "multiple invalid choices", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"kind":"answer","message":"missing changes"}`),
			completionChoiceWithContent(1, "stop", `{"kind":"answer","message":"also missing changes"}`)),
			code: ErrorInvalidResponse},
		{name: "answer with changes", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"kind":"answer","message":"unsafe shape","changes":[`+
				`{"field":"cpus","value":"2","unset":false,"citation":"cpus"}]}`)),
			code: ErrorInvalidResponse},
		{name: "recommendation without changes", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop",
				`{"kind":"recommendation","message":"missing changes","changes":[]}`)),
			code: ErrorInvalidResponse},
		{name: "missing unset", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"kind":"recommendation","message":"missing unset","changes":[`+
				`{"field":"cpus","value":"2","citation":"cpus"}]}`)), code: ErrorInvalidResponse},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			transport := staticCompletionTransport(test.body)
			session, err := newSession(testConfig(ProviderOpenAI, ""), transport)
			if err != nil {
				t.Fatalf("newSession() error = %v", err)
			}
			t.Cleanup(session.Close)
			_, err = session.Recommend(t.Context(), testProjection(), testQuestion)
			if !isActionError(err, test.code) {
				t.Fatalf("Recommend() error = %v, want %s", err, test.code)
			}
		})
	}
}

func testConfig(provider Provider, endpoint string) Config {
	return Config{
		Provider: provider, Model: testModel, Endpoint: endpoint,
		Timeout: 5 * time.Second, APIKey: testAPIKey,
	}
}

func testProjection() application.AssistProjection {
	return application.AssistProjection{
		Version: application.AssistProjectionVersion,
		Project: "project", Service: "api", Runtime: "docker",
		PlatformOS: "linux", PlatformArch: "amd64", Action: "upgrade",
		Fields: []application.DeploymentFieldState{{
			ID: "cpus", Value: "1", Present: true, AllowsUnset: true, Available: true,
		}},
		Identity: strings.Repeat("a", 64),
	}
}

func completionResponse(request *http.Request, model string) *http.Response {
	return completionResponseWithBody(request, completionBody(model))
}

func completionResponseWithBody(request *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{contentTypeHeader: []string{jsonContentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    request,
	}
}

func staticCompletionTransport(body string) http.RoundTripper {
	return roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{contentTypeHeader: []string{jsonContentType}},
			Body:       io.NopCloser(strings.NewReader(body)), Request: request,
		}, nil
	})
}

func completionBody(model string) string {
	return completionBodyWithChoices(model, completionChoice(0, "stop"))
}

func completionBodyWithChoices(model string, choices ...string) string {
	return `{"id":"completion","object":"chat.completion","created":1,"model":"` + model +
		`","choices":[` + strings.Join(choices, ",") + `]}`
}

func completionChoice(index int, finish string) string {
	content := `{"kind":"recommendation","message":"Set a bounded CPU limit","changes":[` +
		`{"field":"cpus","value":"2","unset":false,"citation":"cpus"}]}`

	return completionChoiceWithContent(index, finish, content)
}

func completionChoiceWithContent(index int, finish string, content string) string {
	encoded, _ := json.Marshal(content)

	return `{"index":` + strconv.Itoa(index) + `,"message":{"role":"assistant","content":` +
		string(encoded) + `},"finish_reason":"` + finish + `"}`
}

func FuzzValidateRecommendation(fuzz *testing.F) {
	fuzz.Add(`{"kind":"recommendation","message":"Set a bounded CPU limit","changes":[` +
		`{"field":"cpus","value":"2","unset":false,"citation":"cpus"}]}`)
	fuzz.Add(`{"kind":"answer","message":"No change is needed","changes":[]}`)
	fuzz.Add(`{"kind":"answer","message":"missing changes"}`)
	fuzz.Add("")
	fuzz.Fuzz(func(t *testing.T, content string) {
		choice := anyLLMTestChoice(content)
		response, canonical, err := validateChoice(choice, testProjection())
		if err != nil {
			return
		}
		if response.Message == "" || !validChoiceShape(response.Kind, len(response.Changes)) || canonical == "" {
			t.Fatalf("accepted incomplete response: %#v, %q", response, canonical)
		}
	})
}

func anyLLMTestChoice(content string) anyllm.Choice {
	return anyllm.Choice{
		Index: 0, FinishReason: anyllm.FinishReasonStop,
		Message: anyllm.Message{Role: anyllm.RoleAssistant, Content: content},
	}
}

func assertRecommendationResult(t *testing.T, result Result) {
	t.Helper()
	if len(result.Choices) != 1 || result.Choices[0].Kind != ChoiceRecommendation ||
		len(result.Choices[0].Changes) != 1 ||
		result.Choices[0].Changes[0] != (Change{FieldID: "cpus", Value: "2"}) {
		t.Fatalf("result = %#v", result)
	}
}

//nolint:cyclop // This helper checks one complete adapter request contract.
func assertProductRequestContract(t *testing.T, body []byte, expectAssistant bool) {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	outputTokens := request["max_completion_tokens"]
	if outputTokens == nil {
		outputTokens = request["max_tokens"]
	}
	if request["reasoning_effort"] != "none" || outputTokens != float64(8192) {
		t.Fatalf("request controls = %#v", request)
	}
	messages, valid := request["messages"].([]any)
	if !valid || len(messages) == 0 {
		t.Fatalf("messages = %#v", request["messages"])
	}
	assistantFound := false
	for _, value := range messages {
		message, _ := value.(map[string]any)
		if message["role"] != "assistant" {
			continue
		}
		assistantFound = true
		if _, found := message["tool_calls"]; found {
			t.Fatalf("assistant message contains absent tool_calls: %#v", message)
		}
	}
	if assistantFound != expectAssistant {
		t.Fatalf("assistant message present = %t, want %t", assistantFound, expectAssistant)
	}
	if bytes.Contains(body, []byte(testAPIKey)) {
		t.Fatal("request body contains API key")
	}
}

func isActionError(err error, code ErrorCode) bool {
	action, valid := errors.AsType[*ActionError](err)

	return valid && action.Code == code
}
