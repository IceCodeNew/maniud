package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/llm"
	"github.com/IceCodeNew/maniud/internal/tui"
)

const (
	testLLMChangedModel = "changed-model"
	testLLMCredential   = "credential"
	testLLMIdentity     = "identity"
	testLLMKey          = "key"
	testLLMModelValue   = "model"
	testLLMSecretValue  = "secret"
	testLLMValue        = "value"
)

var errTUIAssistantFixture = errors.New("TUI assistant fixture failure")

type assistantOperationsFixture struct {
	snapshot func(context.Context, application.Request) (application.OperationSnapshot, error)
}

type delayedCancellationContext struct {
	failAt int32
	calls  atomic.Int32
}

func (*delayedCancellationContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (*delayedCancellationContext) Done() <-chan struct{}       { return nil }
func (*delayedCancellationContext) Value(any) any               { return nil }

func (ctx *delayedCancellationContext) Err() error {
	if ctx.calls.Add(1) >= ctx.failAt {
		return context.Canceled
	}

	return nil
}

func (*assistantOperationsFixture) DryRun(context.Context, application.Request) (application.Plan, error) {
	return application.Plan{}, nil
}

func (*assistantOperationsFixture) Apply(context.Context, application.Request) (application.Plan, error) {
	return application.Plan{}, nil
}

func (*assistantOperationsFixture) ResolveHealth(
	context.Context,
	application.Request,
	application.HealthResolution,
) (application.Plan, error) {
	return application.Plan{}, nil
}

func (fixture *assistantOperationsFixture) Snapshot(
	ctx context.Context,
	request application.Request,
) (application.OperationSnapshot, error) {
	snapshot, err := fixture.snapshot(ctx, request)
	if err == nil && snapshot.Plan.Source == (domain.Digest{}) {
		snapshot.Plan.Source = domain.Hash(request.Source.Content)
		if request.Source.Repository != nil {
			snapshot.Plan.Source = request.Source.Repository.Digest
		}
	}

	return snapshot, err
}

func (*assistantOperationsFixture) Evidence(application.OperationSnapshot) (application.EvidenceBundle, error) {
	return application.EvidenceBundle{}, nil
}

func TestTUIAssistContextBindsCleanSourceAndPrivateValues(t *testing.T) {
	t.Parallel()
	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	operations := &assistantOperationsFixture{snapshot: func(
		context.Context,
		application.Request,
	) (application.OperationSnapshot, error) {
		return validAssistantSnapshot(), nil
	}}
	first, err := workspace.assistContext(t.Context(), request, operations)
	if err != nil {
		t.Fatalf("assistContext() error = %v", err)
	}
	second, err := workspace.assistContext(t.Context(), request, operations)
	if err != nil || first.identity != second.identity || first.projection.Identity != second.projection.Identity {
		t.Fatalf("assistContext(second) = %#v, %v", second, err)
	}
	if !containsForbiddenValue(first.forbidden, "image reference", "example.com/team/api:1") ||
		!containsForbiddenValue(first.forbidden, "command", "true") {
		t.Fatalf("forbidden = %#v", first.forbidden)
	}
	changedSnapshot := validAssistantSnapshot()
	changedSnapshot.Runtime.Digest = domain.Hash([]byte("changed runtime evidence"))
	operations.snapshot = func(context.Context, application.Request) (application.OperationSnapshot, error) {
		return changedSnapshot, nil
	}
	changed, err := workspace.assistContext(t.Context(), request, operations)
	if err != nil || changed.identity == first.identity {
		t.Fatalf("assistContext(changed runtime) = %#v, %v", changed, err)
	}
}

func TestTUIAssistContextRejectsSourceDriftDuringSnapshot(t *testing.T) {
	t.Parallel()
	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	operations := &assistantOperationsFixture{snapshot: func(
		context.Context,
		application.Request,
	) (application.OperationSnapshot, error) {
		path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
		drifted := append(append([]byte(nil), request.Source.Content...), []byte("# drift\n")...)
		if err := os.WriteFile(path, drifted, 0o600); err != nil {
			t.Fatal(err)
		}

		return validAssistantSnapshot(), nil
	}}
	if _, err := workspace.assistContext(t.Context(), request, operations); err == nil {
		t.Fatal("assistContext() succeeded after source drift")
	}
}

func TestTUIAssistantCloseClearsOnlyResolvedSecrets(t *testing.T) {
	t.Parallel()
	assistant := defaultTUIAssistant(nil, "", nil, nil)
	assistant.resolved = llmResolvedConfig{
		config: llm.Config{
			Provider: llm.ProviderOpenAI, Model: testLLMModelValue,
			Timeout: time.Minute, APIKey: testLLMSecretValue,
		},
		identity: testLLMIdentity, keySource: "process environment",
		secrets:  []string{testLLMSecretValue},
		warnings: []string{"current .env was ignored"},
		baseline: llmConfigBaseline{initialized: true},
	}

	assistant.Close()

	if assistant.resolved.config.APIKey != "" || assistant.resolved.secrets != nil {
		t.Fatalf("Close() retained resolved secrets: %#v", assistant.resolved)
	}
	if assistant.resolved.identity != testLLMIdentity ||
		assistant.resolved.keySource != "process environment" ||
		!slices.Equal(assistant.resolved.warnings, []string{"current .env was ignored"}) ||
		!assistant.resolved.baseline.initialized {
		t.Fatalf("Close() removed non-sensitive state: %#v", assistant.resolved)
	}
}

func TestTUIPendingAssistContextRejectsUnavailableInputs(t *testing.T) {
	t.Parallel()
	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := workspace.pendingAssistContext(t.Context(), request, nil); err == nil {
		t.Fatal("pendingAssistContext(nil operations) succeeded")
	}
	workspace.staged = &tuiStagedDeployment{}
	if _, err := workspace.pendingAssistContext(
		t.Context(), request, &assistantOperationsFixture{},
	); err == nil {
		t.Fatal("pendingAssistContext(staged edit) succeeded")
	}
}

func TestTUIAssistContextHonorsCancellationAtEveryLoadBoundary(t *testing.T) {
	t.Parallel()
	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	operations := &assistantOperationsFixture{snapshot: func(
		context.Context,
		application.Request,
	) (application.OperationSnapshot, error) {
		return validAssistantSnapshot(), nil
	}}
	for failAt := int32(1); failAt <= 32; failAt++ {
		ctx := &delayedCancellationContext{failAt: failAt}
		_, _ = workspace.assistContext(ctx, request, operations)
	}
}

func TestTUIAssistContextBoundaries(t *testing.T) {
	t.Parallel()
	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := workspace.assistContext(t.Context(), request, nil); err == nil {
		t.Fatal("assistContext(nil operations) succeeded")
	}
	workspace.draft = &tuiDeploymentDraft{}
	if _, err := workspace.assistContext(t.Context(), request, &assistantOperationsFixture{}); err == nil {
		t.Fatal("assistContext(active draft) succeeded")
	}
	workspace.draft = nil
	workspace.staged = &tuiStagedDeployment{}
	if _, err := workspace.assistContext(t.Context(), request, &assistantOperationsFixture{}); err == nil {
		t.Fatal("assistContext(staged edit) succeeded")
	}
	workspace.staged = nil

	snapshotFailure := &assistantOperationsFixture{snapshot: func(
		context.Context,
		application.Request,
	) (application.OperationSnapshot, error) {
		return application.OperationSnapshot{}, errTUIAssistantFixture
	}}
	if _, err := workspace.assistContext(t.Context(), request, snapshotFailure); err == nil {
		t.Fatal("assistContext(snapshot failure) succeeded")
	}
	invalidSnapshot := &assistantOperationsFixture{snapshot: func(
		context.Context,
		application.Request,
	) (application.OperationSnapshot, error) {
		return application.OperationSnapshot{}, nil
	}}
	if _, err := workspace.assistContext(t.Context(), request, invalidSnapshot); err == nil {
		t.Fatal("assistContext(invalid snapshot) succeeded")
	}
	invalidRequest := request
	invalidRequest.Service = "missing"
	if _, err := workspace.assistContext(t.Context(), invalidRequest, snapshotFailure); err == nil {
		t.Fatal("assistContext(invalid request) succeeded")
	}
}

func TestTUIRecommendationPreviewBoundaries(t *testing.T) {
	t.Parallel()
	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())

	if _, err := workspace.PreviewPatches(t.Context(), request, nil); !isDeploymentFailure(
		err, tui.DeploymentUnsupportedSource,
	) {
		t.Fatalf("PreviewPatches(empty) error = %v", err)
	}
	tooMany := make([]application.DeploymentPatch, len(application.DeploymentFields())+1)
	if _, err := workspace.PreviewPatches(t.Context(), request, tooMany); !isDeploymentFailure(
		err, tui.DeploymentUnsupportedSource,
	) {
		t.Fatalf("PreviewPatches(too many) error = %v", err)
	}
	if _, err := workspace.PreviewPatches(
		t.Context(), request, []application.DeploymentPatch{{}},
	); !isDeploymentFailure(err, tui.DeploymentUnsupportedSource) {
		t.Fatalf("PreviewPatches(invalid patch) error = %v", err)
	}
	workspace.staged = &tuiStagedDeployment{}
	if _, err := workspace.PreviewPatches(t.Context(), request, []application.DeploymentPatch{
		deploymentPatch(t, application.DeploymentCPUs, "2"),
	}); !isDeploymentFailure(err, tui.DeploymentPreconditionFailed) {
		t.Fatalf("PreviewPatches(staged edit) error = %v", err)
	}
}

//nolint:cyclop // Every private Compose and runtime value category has a distinct assertion.
func TestAssistForbiddenValuesCoversPrivateInventory(t *testing.T) {
	t.Parallel()
	content := strings.Replace(string(deploymentComposeFixture()), "    network_mode: bridge\n", `    network_mode: bridge
    environment:
      PRIVATE_VALUE: compose-private-value
      EMPTY_VALUE: ""
    ports:
      - "127.0.0.1:8080:80/tcp"
    volumes:
      - type: volume
        target: /data
    devices:
      - /dev/null:/dev/private
`, 1)
	_, request, repository := newTUIDeploymentWorkspaceFixture(t, []byte(content))
	invalid := request.Source
	invalid.Content = []byte("not: [valid")
	if _, err := assistForbiddenValues(t.Context(), invalid, request.Service, validAssistantSnapshot()); err == nil {
		t.Fatal("assistForbiddenValues(invalid source) succeeded")
	}
	if _, err := assistForbiddenValues(t.Context(), request.Source, "missing", validAssistantSnapshot()); err == nil {
		t.Fatal("assistForbiddenValues(missing service) succeeded")
	}
	snapshot := validAssistantSnapshot()
	snapshot.Plan.Observation.RuntimeMounts = []domain.RuntimeMount{{Name: "volume-name", Source: "/runtime/source"}}
	snapshot.Plan.Image = domain.ImageIdentity{
		Environment: []string{"IMAGE_PRIVATE=image-private-value"},
		Entrypoint:  []string{"image-private-entrypoint"},
		Command:     []string{"image-private-command"},
		Healthcheck: &domain.Healthcheck{Test: []string{"image-private-healthcheck"}},
	}
	values, err := assistForbiddenValues(t.Context(), request.Source, request.Service, snapshot)
	if err != nil || !containsForbiddenValue(values, "private path", repository) ||
		len(values["runtime ID"]) < 2 || !containsForbiddenValue(values, "port", "8080") ||
		!containsForbiddenValue(values, "port", "80") || len(values["mount"]) < 2 ||
		len(values["device"]) < 2 ||
		!containsForbiddenValue(values, "environment", "PRIVATE_VALUE=compose-private-value") ||
		!containsForbiddenValue(values, "environment", "compose-private-value") ||
		!containsForbiddenValue(values, "environment", "EMPTY_VALUE=") ||
		!containsForbiddenValue(values, "environment", "IMAGE_PRIVATE=image-private-value") ||
		!containsForbiddenValue(values, "environment", "image-private-value") ||
		!containsForbiddenValue(values, "command", "image-private-entrypoint") ||
		!containsForbiddenValue(values, "command", "image-private-command") ||
		!containsForbiddenValue(values, "command", "image-private-healthcheck") {
		t.Fatalf("assistForbiddenValues() = %#v, %v", values, err)
	}
	archive := compose.Source{Content: []byte(assistantArchiveComposeFixture()), WorkingDir: t.TempDir()}
	if _, err = assistForbiddenValues(t.Context(), archive, "api", snapshot); err != nil {
		t.Fatalf("assistForbiddenValues(archive) error = %v", err)
	}
	invalidImage := compose.Source{
		Content: []byte(strings.Replace(
			string(deploymentComposeFixture()), "    image: example.com/team/api:1\n",
			"    image: https://invalid.example/team/api@sha256:"+strings.Repeat("b", 64)+"\n", 1,
		)),
		WorkingDir: t.TempDir(),
	}
	if _, err = assistForbiddenValues(t.Context(), invalidImage, "api", snapshot); err != nil {
		t.Fatalf("assistForbiddenValues(invalid image) error = %v", err)
	}
}

func TestTUIAssistantRejectsKnownForbiddenQuestionCategories(t *testing.T) {
	t.Parallel()
	forbidden := map[string][]string{
		testLLMCredential: {"secret-value"},
		"environment":     {"PRIVATE_VALUE=compose-private-value"},
		"private path":    {"/private/repository"},
		"image reference": {"example.com/team/api:1"},
		"command":         {"serve --private"},
		"port":            {"127.0.0.1:8080:80/tcp"},
		"mount":           {"/private/data"},
		"device":          {"/dev/private"},
		"runtime ID":      {"runtime-object-123"},
	}
	for category, values := range forbidden {
		question := "Please use " + values[0]
		if got := forbiddenQuestionCategory(question, forbidden); got != category {
			t.Fatalf("forbiddenQuestionCategory(%q) = %q, want %q", question, got, category)
		}
	}
	if got := forbiddenQuestionCategory("Recommend a conservative memory limit", forbidden); got != "" {
		t.Fatalf("safe question category = %q", got)
	}
}

//nolint:cyclop // The workflow test covers staging, discard, cleanliness, and duplicate rejection in order.
func TestTUIDeploymentWorkspacePreviewsMultiFieldRecommendation(t *testing.T) {
	t.Parallel()
	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	preview, err := workspace.PreviewPatches(t.Context(), request, []application.DeploymentPatch{
		deploymentPatch(t, application.DeploymentCPUs, testDeploymentCPUValue),
		deploymentPatch(t, application.DeploymentMemory, "2048"),
	})
	if err != nil || len(preview.Changes) != 2 ||
		preview.Changes[0].CurrentValue != "1" || preview.Changes[0].ProposedValue != testDeploymentCPUValue ||
		preview.Changes[1].CurrentValue != "512" || preview.Changes[1].ProposedValue != "2048" {
		t.Fatalf("PreviewPatches() = %#v, %v", preview, err)
	}
	staged, err := workspace.Stage(t.Context())
	if err != nil || !strings.Contains(staged.Diff, "+    cpus: 2.5") ||
		!strings.Contains(staged.Diff, "+    mem_limit: 2048") {
		t.Fatalf("Stage() = %#v, %v", staged, err)
	}
	if err = workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard() error = %v", err)
	}
	if _, err = cleanGitTree(t.Context(), repository); err != nil {
		t.Fatalf("repository is not clean: %v", err)
	}
	if _, err = workspace.PreviewPatches(t.Context(), request, []application.DeploymentPatch{
		deploymentPatch(t, application.DeploymentCPUs, "2"),
		deploymentPatch(t, application.DeploymentCPUs, "3"),
	}); err == nil {
		t.Fatal("PreviewPatches(duplicate) succeeded")
	}
}

func containsForbiddenValue(values map[string][]string, category, expected string) bool {
	return slices.Contains(values[category], expected)
}

func validAssistantSnapshot() application.OperationSnapshot {
	platform := domain.Platform{OS: "linux", Architecture: "amd64"}

	return application.OperationSnapshot{
		CapturedAt: time.Unix(1, 0).UTC(),
		Plan: application.Plan{
			Kind: application.PlanBootstrap, Project: "example", Service: applyServiceValue,
			Runtime: domain.RuntimeDocker, Platform: platform, Desired: domain.Hash([]byte("desired")),
			Observation: application.WorkloadObservation{State: application.WorkloadObservationMissing},
		},
		Runtime: application.RuntimeEvidence{
			Kind: domain.RuntimeDocker, Platform: platform, Digest: domain.Hash([]byte("runtime")),
		},
	}
}

func TestPublicLLMConfigurationErrorsRemainPrivacySafe(t *testing.T) {
	t.Parallel()
	for input, expected := range map[error]string{
		errLLMConfigSaveUnknown: "config_save_outcome_unknown",
		errLLMConfigSaveStale:   "config_save_stale",
		errLLMConfigPathInvalid: "llm_config_path_invalid",
	} {
		if got := publicLLMConfigError(input).Error(); got != expected {
			t.Fatalf("publicLLMConfigError(%v) = %q", input, got)
		}
	}
}

//nolint:cyclop,funlen,gocognit,gocyclo,maintidx,paralleltest // The contract test replaces the global transport.
func TestTUIAssistantRecommendsAndAcceptsWithPinnedCompatibleAdapter(t *testing.T) {
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	var providerFailure atomic.Bool
	var driftSource atomic.Bool
	var driftRuntime atomic.Bool
	var historyLimitResponse atomic.Bool
	var providerRequests atomic.Int64
	var sourcePath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		providerRequests.Add(1)
		if request.URL.Path != "/chat/completions" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		_, _ = io.Copy(io.Discard, request.Body)
		response.Header().Set("Content-Type", "application/json")
		if providerFailure.Load() {
			response.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(response, `{"error":{"message":"private","type":"authentication_error"}}`)

			return
		}
		if driftSource.Load() {
			if err := os.WriteFile(sourcePath, append(deploymentComposeFixture(), []byte("# drift\n")...), 0o600); err != nil {
				t.Errorf("drift source: %v", err)
			}
		}
		if historyLimitResponse.Load() {
			_, _ = io.WriteString(response, assistantHistoryLimitCompletionBody("assistant-test-model"))

			return
		}
		_, _ = io.WriteString(response, assistantCompletionBody("assistant-test-model"))
	}))
	t.Cleanup(server.Close)
	http.DefaultTransport = server.Client().Transport

	content := bytes.Replace(
		deploymentComposeFixture(),
		[]byte("    network_mode: bridge\n"),
		[]byte("    network_mode: bridge\n    environment:\n      PRIVATE_VALUE: compose-private-value\n"),
		1,
	)
	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, content)
	sourcePath = filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	operations := &assistantOperationsFixture{snapshot: func(
		context.Context,
		application.Request,
	) (application.OperationSnapshot, error) {
		snapshot := validAssistantSnapshot()
		snapshot.Plan.Image = domain.ImageIdentity{
			Environment: []string{"IMAGE_PRIVATE=image-private-value"},
			Command:     []string{"image-private-command"},
		}
		if driftRuntime.Load() {
			snapshot.Runtime.Digest = domain.Hash([]byte("changed runtime evidence"))
		}

		return snapshot, nil
	}}
	environment := map[string]string{
		homeEnvironment: t.TempDir(), llmProviderEnvironment: string(llm.ProviderOpenAICompatible),
		llmModelEnvironment: "assistant-test-model", llmTimeoutEnvironment: "5",
		openAIEndpointEnvironment: server.URL, openAIKeyEnvironment: "assistant-test-key",
	}
	assistant := defaultTUIAssistant(environment, repository, workspace, operations)
	configuration, err := assistant.Configuration(t.Context())
	if err != nil || !configuration.Complete {
		t.Fatalf("Configuration() = %#v, %v", configuration, err)
	}
	if _, err = assistant.Recommend(
		t.Context(), request, configuration.Identity, "Use compose-private-value as the limit",
	); err == nil || providerRequests.Load() != 0 {
		t.Fatalf("Recommend(environment value) error = %v, provider requests = %d", err, providerRequests.Load())
	}
	for _, question := range []string{
		"Use image-private-value as the limit",
		"Run image-private-command before startup",
	} {
		if _, err = assistant.Recommend(
			t.Context(), request, configuration.Identity, question,
		); err == nil || providerRequests.Load() != 0 {
			t.Fatalf("Recommend(%q) error = %v, provider requests = %d", question, err, providerRequests.Load())
		} else if action, valid := errors.AsType[*llm.ActionError](err); !valid ||
			action.Stage != llm.ActionStageRequestPreparation || action.RequestOutcome != llm.RequestNotStarted {
			t.Fatalf("Recommend(%q) preflight outcome = %#v", question, action)
		}
	}
	http.DefaultTransport = nil
	failingAssistant := defaultTUIAssistant(environment, repository, workspace, operations)
	if _, err = failingAssistant.Recommend(
		t.Context(), request, configuration.Identity, "Recommend a bounded CPU limit",
	); err == nil {
		t.Fatal("Recommend(provider construction failure) succeeded")
	} else if action, valid := errors.AsType[*llm.ActionError](err); !valid ||
		action.Stage != llm.ActionStageRequestPreparation || action.RequestOutcome != llm.RequestNotStarted {
		t.Fatalf("provider construction outcome = %#v", action)
	}
	http.DefaultTransport = server.Client().Transport
	result, err := assistant.Recommend(
		t.Context(), request, configuration.Identity, "Recommend a bounded CPU limit",
	)
	if err != nil || len(result.Choices) != 1 {
		t.Fatalf("Recommend() = %#v, %v", result, err)
	}
	patches := testDeploymentPatches(t, result.Choices[0].Changes)
	if _, err = workspace.PreviewPatches(t.Context(), request, patches); err != nil {
		t.Fatalf("PreviewPatches() error = %v", err)
	}
	if err = assistant.Accept(t.Context(), result.Token, 1); err == nil {
		t.Fatal("Accept(invalid choice) succeeded")
	}
	if err = assistant.Accept(t.Context(), result.Token, 0); err != nil {
		t.Fatalf("Accept() error = %v", err)
	}
	if err = workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard(accepted preview) error = %v", err)
	}
	staleResult, err := assistant.Recommend(
		t.Context(), request, configuration.Identity, "Recommend with fresh runtime evidence",
	)
	if err != nil {
		t.Fatalf("Recommend(before accept drift) error = %v", err)
	}
	patches = testDeploymentPatches(t, staleResult.Choices[0].Changes)
	if _, err = workspace.PreviewPatches(t.Context(), request, patches); err != nil {
		t.Fatalf("PreviewPatches(before accept drift) error = %v", err)
	}
	driftRuntime.Store(true)
	err = assistant.Accept(t.Context(), staleResult.Token, 0)
	action, valid := errors.AsType[*llm.ActionError](err)
	if !valid || action.Code != llm.ErrorContextStale || assistant.session != nil || assistant.pending != nil {
		t.Fatalf("Accept(runtime drift) = %v, session = %#v, pending = %#v", err, assistant.session, assistant.pending)
	}
	if err = workspace.Discard(t.Context()); err != nil || workspace.draft != nil {
		t.Fatalf("Discard(stale preview) = %v, draft = %#v", err, workspace.draft)
	}
	driftRuntime.Store(false)
	if err = assistant.Accept(t.Context(), staleResult.Token, 0); err == nil {
		t.Fatal("Accept(stale token after runtime drift) succeeded")
	}
	providerFailure.Store(true)
	if _, err = assistant.Recommend(
		t.Context(), request, configuration.Identity, "Recommend another bounded CPU limit",
	); err == nil {
		t.Fatal("Recommend(provider failure) succeeded")
	} else if action, valid := errors.AsType[*llm.ActionError](err); !valid ||
		action.Stage != llm.ActionStageProviderRequest || action.RequestOutcome != llm.RequestResponseReceived {
		t.Fatalf("provider response outcome = %#v", action)
	}
	providerFailure.Store(false)
	if err = assistant.Accept(t.Context(), result.Token, 0); err == nil {
		t.Fatal("Accept(reused token) succeeded")
	}
	for turn := range 8 {
		turnResult, turnErr := assistant.Recommend(
			t.Context(), request, configuration.Identity, fmt.Sprintf("Conversation turn %d", turn),
		)
		if turnErr != nil {
			t.Fatalf("Recommend(turn %d) error = %v", turn, turnErr)
		}
		if turnErr = assistant.Accept(t.Context(), turnResult.Token, 0); turnErr != nil {
			t.Fatalf("Accept(turn %d) error = %v", turn, turnErr)
		}
	}
	_, err = assistant.Recommend(
		t.Context(), request, configuration.Identity, "Start a fresh conversation",
	)
	action, valid = errors.AsType[*llm.ActionError](err)
	if !valid || action.Code != llm.ErrorConversationLimit || assistant.session != nil ||
		assistant.pending != nil {
		t.Fatalf("conversation limit = %v, session = %#v, pending = %#v", err, assistant.session, assistant.pending)
	}
	freshResult, err := assistant.Recommend(
		t.Context(), request, configuration.Identity, "First turn in the new conversation",
	)
	if err != nil {
		t.Fatalf("Recommend(new conversation) error = %v", err)
	}
	if err = assistant.Accept(t.Context(), freshResult.Token, 0); err != nil {
		t.Fatalf("Accept(new conversation) error = %v", err)
	}
	historyLimitResponse.Store(true)
	largeResult, err := assistant.Recommend(
		t.Context(), request, configuration.Identity, "Return a large first response",
	)
	if err != nil {
		t.Fatalf("Recommend(first large response) error = %v", err)
	}
	if err = assistant.Accept(t.Context(), largeResult.Token, 0); err != nil {
		t.Fatalf("Accept(first large response) error = %v", err)
	}
	largeResult, err = assistant.Recommend(
		t.Context(), request, configuration.Identity, "Return a large second response",
	)
	if err != nil {
		t.Fatalf("Recommend(second large response) error = %v", err)
	}
	err = assistant.Accept(t.Context(), largeResult.Token, 0)
	action, valid = errors.AsType[*llm.ActionError](err)
	if !valid || action.Code != llm.ErrorConversationLimit || assistant.session != nil ||
		assistant.pending != nil {
		t.Fatalf(
			"Accept(history limit) = %v, session present = %t, pending = %t",
			err, assistant.session != nil, assistant.pending != nil,
		)
	}
	historyLimitResponse.Store(false)
	if _, err = assistant.Recommend(t.Context(), request, "stale", "question"); err == nil {
		t.Fatal("Recommend(stale configuration) succeeded")
	}
	if _, err = assistant.Recommend(t.Context(), request, configuration.Identity, "assistant-test-key"); err == nil {
		t.Fatal("Recommend(secret question) succeeded")
	}
	driftSource.Store(true)
	if _, err = assistant.Recommend(
		t.Context(), request, configuration.Identity, "Recommend after source drift",
	); err == nil {
		t.Fatal("Recommend(source drift) succeeded")
	} else if action, valid := errors.AsType[*llm.ActionError](err); !valid ||
		action.Stage != llm.ActionStageContextValidation || action.RequestOutcome != llm.RequestResponseReceived {
		t.Fatalf("source drift outcome = %#v", action)
	}
	assistant.Close()
	assistant.Close()
}

//nolint:cyclop,funlen // Save publication and reload failures have separate privacy-safe outcomes.
func TestTUIAssistantContainsUnknownSaveAndPostSaveReloadFailure(t *testing.T) {
	t.Parallel()
	settings := tui.LLMSettings{
		Provider: string(llm.ProviderOpenAI), Model: testLLMModelValue, Timeout: "60", APIKey: testLLMKey,
	}
	freshAssistant := func(t *testing.T) *tuiAssistant {
		t.Helper()
		home := t.TempDir()

		return defaultTUIAssistant(map[string]string{
			homeEnvironment: home, xdgConfigHomeEnvironment: filepath.Join(home, "config"),
		}, t.TempDir(), nil, nil)
	}
	newAssistant := func(t *testing.T) *tuiAssistant {
		t.Helper()
		assistant := freshAssistant(t)
		if _, err := assistant.Configuration(t.Context()); err != nil {
			t.Fatal(err)
		}

		return assistant
	}
	direct := freshAssistant(t)
	if configuration, err := direct.Save(t.Context(), settings); err != nil || !configuration.Complete {
		t.Fatalf("Save(initial load) = %#v, %v", configuration, err)
	}

	unknown := newAssistant(t)
	unknown.configOps.openDirectory = func(*os.Root, string) (*os.File, error) {
		return nil, errTUIAssistantFixture
	}
	configuration, err := unknown.Save(t.Context(), settings)
	actionError, valid := errors.AsType[*llm.ActionError](err)
	if !valid || actionError.Code != tui.LLMConfigSaveUnknown || !configuration.Complete {
		t.Fatalf("Save(unknown) = %#v, %v", configuration, err)
	}
	unknownReload := newAssistant(t)
	unknownCtx, cancelUnknown := context.WithCancel(t.Context())
	unknownReload.configOps.openDirectory = func(*os.Root, string) (*os.File, error) {
		cancelUnknown()

		return nil, errTUIAssistantFixture
	}
	configuration, err = unknownReload.Save(unknownCtx, settings)
	actionError, valid = errors.AsType[*llm.ActionError](err)
	if !valid || actionError.Code != tui.LLMConfigSaveUnknown || configuration.Complete {
		t.Fatalf("Save(unknown reload failure) = %#v, %v", configuration, err)
	}
	ordinaryFailure := newAssistant(t)
	ordinaryFailure.configOps.rename = func(*os.Root, string, string) error {
		return errTUIAssistantFixture
	}
	configuration, err = ordinaryFailure.Save(t.Context(), settings)
	actionError, valid = errors.AsType[*llm.ActionError](err)
	if !valid || actionError.Code != llm.ErrorConfigInvalid || configuration.Complete {
		t.Fatalf("Save(ordinary publication failure) = %#v, %v", configuration, err)
	}

	reloadFailure := newAssistant(t)
	ctx, cancel := context.WithCancel(t.Context())
	baseRename := reloadFailure.configOps.rename
	reloadFailure.configOps.rename = func(root *os.Root, oldName, newName string) error {
		renameErr := baseRename(root, oldName, newName)
		if renameErr == nil {
			cancel()
		}

		return renameErr
	}
	if _, err = reloadFailure.Save(ctx, settings); err == nil {
		t.Fatal("Save(post-save reload failure) succeeded")
	}
	actionError, valid = errors.AsType[*llm.ActionError](err)
	if !valid || actionError.Code != tui.LLMConfigSavedReloadFailed {
		t.Fatalf("Save(post-save reload failure) error = %v", err)
	}

	stale := newAssistant(t)
	stale.resolved.baseline.state.valid = false
	configuration, err = stale.Save(t.Context(), settings)
	actionError, valid = errors.AsType[*llm.ActionError](err)
	if !valid || actionError.Code != tui.LLMConfigSaveStale ||
		configuration.Provider != "" || !stale.resolved.baseline.state.valid {
		t.Fatalf("Save(stale reload) = %#v, %v", configuration, err)
	}
	stale.resolved.baseline.state.valid = false
	staleCtx, cancelStale := context.WithCancel(t.Context())
	cancelStale()
	_, err = stale.Save(staleCtx, settings)
	actionError, valid = errors.AsType[*llm.ActionError](err)
	if !valid || actionError.Code != tui.LLMConfigReloadFailed {
		t.Fatalf("Save(stale reload failure) error = %v", err)
	}
}

func TestTUIAssistantConfigurationSaveAndPreparationErrors(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	working := t.TempDir()
	environment := map[string]string{
		homeEnvironment: home, xdgConfigHomeEnvironment: filepath.Join(home, "config"),
	}
	assistant := defaultTUIAssistant(environment, working, &tuiDeploymentWorkspace{}, nil)
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := assistant.Configuration(cancelled); err == nil {
		t.Fatal("Configuration(cancelled) succeeded")
	}
	if _, err := assistant.Save(cancelled, tui.LLMSettings{}); err == nil {
		t.Fatal("Save(cancelled initial load) succeeded")
	}
	if _, err := assistant.Configuration(t.Context()); err != nil {
		t.Fatalf("Configuration() error = %v", err)
	}
	if _, err := assistant.Save(t.Context(), tui.LLMSettings{}); err == nil {
		t.Fatal("Save(invalid settings) succeeded")
	}
	assistant.resolved.baseline.state.valid = false
	if _, err := assistant.Save(t.Context(), tui.LLMSettings{
		Provider: string(llm.ProviderOpenAI), Model: testLLMModelValue, Timeout: "60",
	}); err == nil {
		t.Fatal("Save(stale baseline) succeeded")
	}

	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	operations := &assistantOperationsFixture{snapshot: func(
		context.Context,
		application.Request,
	) (application.OperationSnapshot, error) {
		return validAssistantSnapshot(), nil
	}}
	configuredEnvironment := map[string]string{
		homeEnvironment: t.TempDir(), llmProviderEnvironment: string(llm.ProviderOpenAI),
		llmModelEnvironment: testLLMModelValue, llmTimeoutEnvironment: "5", openAIKeyEnvironment: testLLMKey,
	}
	assistant = defaultTUIAssistant(configuredEnvironment, working, workspace, operations)
	configuration, err := assistant.Configuration(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = assistant.Recommend(t.Context(), request, configuration.Identity, ""); err == nil {
		t.Fatal("Recommend(empty question) succeeded")
	}
	workspace.draft = &tuiDeploymentDraft{}
	if _, err = assistant.Recommend(t.Context(), request, configuration.Identity, "question"); err == nil {
		t.Fatal("Recommend(unavailable context) succeeded")
	}
	workspace.draft = nil
	configuredEnvironment[llmModelEnvironment] = testLLMChangedModel
	if _, err = assistant.Configuration(t.Context()); err != nil {
		t.Fatalf("Configuration(changed) error = %v", err)
	}
}

//nolint:cyclop // The table verifies every stable public action-error mapping.
func TestTUIAssistantRejectsDriftAndMapsEveryActionError(t *testing.T) {
	t.Parallel()
	assistant := defaultTUIAssistant(map[string]string{}, "private", nil, nil)
	withoutPrivatePaths := defaultTUIAssistant(nil, "", nil, nil)
	values := withoutPrivatePaths.forbiddenRecommendationValues(llmResolvedConfig{}, tuiAssistContext{})
	if len(values) != 1 {
		t.Fatalf("empty private inventory = %#v", values)
	}
	assistant.resolved = llmResolvedConfig{identity: "first"}
	assistant.sessionID = "session"
	assistant.pending = &tuiAssistantPending{token: "token"}
	assistant.closeSession()
	if assistant.sessionID != "" || assistant.pending != nil {
		t.Fatalf("closed assistant = %#v", assistant)
	}
	cloned := cloneForbiddenValues(map[string][]string{testLLMCredential: {testLLMSecretValue}})
	if len(cloned[testLLMCredential]) != 1 {
		t.Fatalf("cloned forbidden values = %#v", cloned)
	}
	cloned[testLLMCredential][0] = "changed"
	if cloneForbiddenValues(nil) == nil {
		t.Fatal("nil forbidden map did not produce an independent map")
	}
	shortValues := map[string][]string{testLLMCredential: {"x", " "}}
	if category := forbiddenQuestionCategory("x", shortValues); category != testLLMCredential {
		t.Fatalf("short forbidden value matched as %q", category)
	}
	if category := forbiddenQuestionCategory("question", map[string][]string{testLLMCredential: {" "}}); category != "" {
		t.Fatalf("empty forbidden value matched as %q", category)
	}

	codes := []llm.ErrorCode{
		llm.ErrorConfigInvalid, llm.ErrorQuestionInvalid, llm.ErrorConversationLimit, llm.ErrorForbiddenValue,
		llm.ErrorAuthentication, llm.ErrorRateLimited, llm.ErrorContextLimit,
		llm.ErrorRefused, llm.ErrorEmptyResponse, llm.ErrorTruncated,
		llm.ErrorInvalidResponse, llm.ErrorModelUnavailable, llm.ErrorTimeout,
		llm.ErrorCancelled, llm.ErrorContextStale, llm.ErrorProvider, llm.ErrorCode("unknown"),
	}
	for _, code := range codes {
		mapped := publicLLMActionError(&llm.ActionError{
			Code: code, Category: "category", Stage: llm.ActionStageProviderResponse,
			RequestOutcome: llm.RequestResponseReceived,
		})
		action, valid := errors.AsType[*llm.ActionError](mapped)
		if !valid || action.Code != code || action.Category != "category" ||
			action.Stage != llm.ActionStageProviderResponse || action.RequestOutcome != llm.RequestResponseReceived {
			t.Fatalf("publicLLMActionError(%s) = %v", code, mapped)
		}
	}
	if got := publicLLMActionError(errTUIAssistantFixture); got.Error() != string(llm.ErrorProvider) {
		t.Fatalf("private error = %v", got)
	}
	if got := publicLLMConfigError(errTUIAssistantFixture); got.Error() != string(llm.ErrorConfigInvalid) {
		t.Fatalf("private config error = %v", got)
	}
}

func assistantCompletionBody(model string) string {
	return `{"id":"completion","object":"chat.completion","created":1,"model":"` + model +
		`","choices":[{"index":0,"message":{"role":"assistant","content":` +
		`"{\"kind\":\"recommendation\",\"message\":\"Set CPUs\",` +
		`\"changes\":[{\"field\":\"cpus\",` +
		`\"value\":\"2\",\"unset\":false,\"citation\":\"cpus\"}]}"},"finish_reason":"stop"}]}`
}

func testDeploymentPatches(t *testing.T, changes []llm.Change) []application.DeploymentPatch {
	t.Helper()
	patches := make([]application.DeploymentPatch, 0, len(changes))
	for _, change := range changes {
		patch, err := application.ParseDeploymentPatch(change.FieldID, change.Value, change.Unset)
		if err != nil {
			t.Fatalf("ParseDeploymentPatch(%q) error = %v", change.FieldID, err)
		}
		patches = append(patches, patch)
	}

	return patches
}

func assistantHistoryLimitCompletionBody(model string) string {
	content := `{"kind":"recommendation","message":"Tune the health interval","changes":[` +
		`{"field":"healthcheck.interval","value":"` + strings.Repeat("0h", 12_000) +
		`1s","unset":false,"citation":"healthcheck.interval"}]}`

	return `{"id":"completion","object":"chat.completion","created":1,"model":` + strconv.Quote(model) +
		`,"choices":[{"index":0,"message":{"role":"assistant","content":` + strconv.Quote(content) +
		`},"finish_reason":"stop"}]}`
}

func assistantArchiveComposeFixture() string {
	return `name: example
services:
  api:
    container_name: example-api
    image: example.com/team/archive:1
    network_mode: bridge
    platform: linux/amd64
    pull_policy: never
x-maniud:
  services:
    api:
      image_source:
        kind: docker-archive
        selector: example.com/team/archive:1
        archive_digest: sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
        archive_size: 10240
        archive_manifest_digest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
        archive_member_index: 0
        platform: linux/amd64
        source_reference: example.com/team/archive:1
        reference_digest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
        platform_manifest_digest: sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb
        image_config_digest: sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc
`
}
