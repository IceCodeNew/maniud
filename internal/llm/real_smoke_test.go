package llm

import (
	"os"
	"testing"
	"time"
)

const realSmokeEnvironment = "MANIUD_LLM_REAL_SMOKE"

// TestRealProviderSmoke is opt-in because it sends a billed request to the configured provider.
// It uses a static non-secret projection and never prints credentials or provider content.
//
//nolint:paralleltest // The opt-in contract intentionally reads one caller-owned process environment.
func TestRealProviderSmoke(t *testing.T) {
	if os.Getenv(realSmokeEnvironment) != "1" {
		t.Skip("set " + realSmokeEnvironment + "=1 to run the real-provider smoke test")
	}
	provider := Provider(os.Getenv("MANIUD_LLM_PROVIDER"))
	key := os.Getenv("OPENAI_API_KEY")
	if provider == ProviderDeepSeek {
		key = os.Getenv("DEEPSEEK_API_KEY")
	}
	config := Config{
		Provider: provider,
		Model:    os.Getenv("MANIUD_LLM_MODEL"),
		Endpoint: os.Getenv("OPENAI_BASE_URL"),
		Timeout:  60 * time.Second,
		APIKey:   key,
	}
	session, err := NewSession(config)
	if err != nil {
		t.Fatal("real-provider session configuration failed")
	}
	t.Cleanup(session.Close)
	result, err := session.Recommend(
		t.Context(), testProjection(), "Recommend one conservative CPU limit for this test service.",
	)
	if err != nil {
		t.Fatal("real-provider recommendation failed")
	}
	if len(result.Choices) == 0 {
		t.Fatal("real-provider recommendation returned no validated choice")
	}
}
