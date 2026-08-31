package llm

import (
	json "encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	"github.com/mozilla-ai/any-llm-go/config"
	"github.com/mozilla-ai/any-llm-go/providers"
	"github.com/mozilla-ai/any-llm-go/providers/azureopenai"
	"github.com/mozilla-ai/any-llm-go/providers/openai"
)

func TestPinnedOpenAIAdapterPreservesExplicitEmptyLogitBias(t *testing.T) {
	t.Parallel()
	requestBody := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestBody <- decodePinRequest(t, request)
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, completionBody(testModel))
	}))
	t.Cleanup(server.Close)
	provider, err := openai.New(
		config.WithAPIKey(testAPIKey), config.WithBaseURL(server.URL),
		config.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("openai.New() error = %v", err)
	}
	_, err = provider.Completion(t.Context(), providers.CompletionParams{
		Model: testModel, Messages: []providers.Message{{Role: providers.RoleUser, Content: "hello"}},
		LogitBias: map[string]int{},
	})
	if err != nil {
		t.Fatalf("Completion() error = %v", err)
	}
	logitBias, found := (<-requestBody)["logit_bias"]
	if !found || !reflect.DeepEqual(logitBias, map[string]any{}) {
		t.Fatalf("logit_bias = %#v, present = %t", logitBias, found)
	}
}

//nolint:cyclop // One fixture compares normalized output and all required request fields.
func TestPinnedOpenAIAndAzureNormalizedResponsesParity(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	bodies := make(map[string]map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		body := decodePinRequest(t, request)
		mu.Lock()
		bodies[request.URL.Path] = body
		mu.Unlock()
		response.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(response, responsesBody(testModel))
	}))
	t.Cleanup(server.Close)
	openAI, err := openai.New(
		config.WithAPIKey(testAPIKey), config.WithBaseURL(server.URL),
		config.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("openai.New() error = %v", err)
	}
	azure, err := azureopenai.New(
		config.WithAPIKey(testAPIKey), config.WithBaseURL(server.URL),
		config.WithHTTPClient(server.Client()),
	)
	if err != nil {
		t.Fatalf("azureopenai.New() error = %v", err)
	}
	instructions := "Keep the answer short"
	maximum := 123
	params := providers.ResponsesParams{
		Model: testModel, Instructions: &instructions, MaxTokens: &maximum,
		Reasoning: providers.ReasoningEffortMedium,
		Input:     []providers.ResponsesInputItem{{Role: providers.RoleUser, Content: "hello"}},
	}
	openAIResult, err := openAI.Responses(t.Context(), params)
	if err != nil {
		t.Fatalf("OpenAI Responses() error = %v", err)
	}
	azureResult, err := azure.Responses(t.Context(), params)
	if err != nil {
		t.Fatalf("Azure Responses() error = %v", err)
	}
	if openAIResult.ID != azureResult.ID || openAIResult.Model != azureResult.Model ||
		openAIResult.Status != azureResult.Status || openAIResult.Output != azureResult.Output {
		t.Fatalf("normalized results differ: openai=%#v azure=%#v", openAIResult, azureResult)
	}
	mu.Lock()
	openAIBody := bodies["/responses"]
	azureBody := bodies["/openai/v1/responses"]
	mu.Unlock()
	for name, body := range map[string]map[string]any{"openai": openAIBody, "azure": azureBody} {
		if body["instructions"] != instructions || body["model"] != testModel ||
			body["max_output_tokens"] != float64(maximum) {
			t.Fatalf("%s Responses request = %#v", name, body)
		}
	}
}

func decodePinRequest(t *testing.T, request *http.Request) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.UnmarshalRead(request.Body, &body); err != nil {
		t.Errorf("decode request: %v", err)

		return nil
	}

	return body
}

func responsesBody(model string) string {
	return `{"id":"response","object":"response","created_at":1,"model":"` + model +
		`","status":"completed","output":[{"id":"message","type":"message",` +
		`"status":"completed","role":"assistant","content":[` +
		`{"type":"output_text","text":"done","annotations":[]}]}]}`
}
