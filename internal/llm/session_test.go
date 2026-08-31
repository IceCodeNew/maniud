package llm

import (
	"bytes"
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
			if attempts != 3 {
				t.Fatalf("attempts = %d, want 3", attempts)
			}
		})
	}
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
	if err = session.Accept(first, 0); err != nil {
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
		{name: "missing summary", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"changes":[]}`)), code: ErrorInvalidResponse},
		{name: "missing changes", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"summary":"missing changes"}`)), code: ErrorInvalidResponse},
		{name: "multiple invalid choices", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"summary":"missing changes"}`),
			completionChoiceWithContent(1, "stop", `{"summary":"also missing changes"}`)), code: ErrorInvalidResponse},
		{name: "missing unset", body: completionBodyWithChoices(testModel,
			completionChoiceWithContent(0, "stop", `{"summary":"missing unset","changes":[`+
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
		Fields: []application.AssistField{{
			ID: "cpus", Value: "1", Present: true, AllowsUnset: true, Available: true,
		}},
		Identity: strings.Repeat("a", 64),
	}
}

func completionResponse(request *http.Request, model string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{contentTypeHeader: []string{jsonContentType}},
		Body:       io.NopCloser(strings.NewReader(completionBody(model))),
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
	content := `{"summary":"Set a bounded CPU limit","changes":[` +
		`{"field":"cpus","value":"2","unset":false,"citation":"cpus"}]}`

	return completionChoiceWithContent(index, finish, content)
}

func completionChoiceWithContent(index int, finish string, content string) string {
	encoded, _ := json.Marshal(content)

	return `{"index":` + strconv.Itoa(index) + `,"message":{"role":"assistant","content":` +
		string(encoded) + `},"finish_reason":"` + finish + `"}`
}

func FuzzValidateRecommendation(fuzz *testing.F) {
	fuzz.Add(`{"summary":"Set a bounded CPU limit","changes":[` +
		`{"field":"cpus","value":"2","unset":false,"citation":"cpus"}]}`)
	fuzz.Add(`{"summary":"missing changes"}`)
	fuzz.Add("")
	fuzz.Fuzz(func(t *testing.T, content string) {
		choice := anyLLMTestChoice(content)
		recommendation, canonical, err := validateChoice(choice, testProjection())
		if err != nil {
			return
		}
		if recommendation.Summary == "" || len(recommendation.Changes) == 0 || canonical == "" {
			t.Fatalf("accepted incomplete recommendation: %#v, %q", recommendation, canonical)
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
	if len(result.Choices) != 1 || len(result.Choices[0].Changes) != 1 ||
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
	var action *ActionError

	return errors.As(err, &action) && action.Code == code
}
