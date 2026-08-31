package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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

//nolint:cyclop // Each façade precondition fails before any provider request.
func TestTUIAssistContextAndRecommendationPreviewBoundaries(t *testing.T) {
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

	if _, err := workspace.PreviewRecommendation(t.Context(), request, nil); err == nil {
		t.Fatal("PreviewRecommendation(empty) succeeded")
	}
	tooMany := make([]tui.LLMChange, len(application.DeploymentFields())+1)
	if _, err := workspace.PreviewRecommendation(t.Context(), request, tooMany); err == nil {
		t.Fatal("PreviewRecommendation(too many) succeeded")
	}
	if _, err := workspace.PreviewRecommendation(t.Context(), request, []tui.LLMChange{{
		FieldID: application.DeploymentCPUs.ID(), Value: testInvalidValue,
	}}); err == nil {
		t.Fatal("PreviewRecommendation(invalid patch) succeeded")
	}
	workspace.staged = &tuiStagedDeployment{}
	if _, err := workspace.PreviewRecommendation(t.Context(), request, []tui.LLMChange{{
		FieldID: application.DeploymentCPUs.ID(), Value: "2",
	}}); err == nil {
		t.Fatal("PreviewRecommendation(staged edit) succeeded")
	}
}

//nolint:cyclop // Every private Compose and runtime value category has a distinct assertion.
func TestAssistForbiddenValuesCoversPrivateInventory(t *testing.T) {
	t.Parallel()
	content := strings.Replace(string(deploymentComposeFixture()), "    network_mode: bridge\n", `    network_mode: bridge
    ports:
      - "127.0.0.1:8080:80/tcp"
    volumes:
      - type: volume
        target: /data
    devices:
      - /dev/null:/dev/private
`, 1)
	workspace, request, _ := newTUIDeploymentWorkspaceFixture(t, []byte(content))
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
	values, err := assistForbiddenValues(t.Context(), request.Source, request.Service, snapshot)
	if err != nil || !containsForbiddenValue(values, "private path", workspace.registrationPath) &&
		len(values["runtime ID"]) < 2 || !containsForbiddenValue(values, "port", "8080") ||
		!containsForbiddenValue(values, "port", "80") || len(values["mount"]) < 2 ||
		len(values["device"]) < 2 {
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

func TestTUIDeploymentWorkspacePreviewsMultiFieldRecommendation(t *testing.T) {
	t.Parallel()
	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	preview, err := workspace.PreviewRecommendation(t.Context(), request, []tui.LLMChange{
		{FieldID: application.DeploymentCPUs.ID(), Value: "2.5"},
		{FieldID: application.DeploymentMemory.ID(), Value: "2048"},
	})
	if err != nil || len(preview.FieldIDs) != 2 {
		t.Fatalf("PreviewRecommendation() = %#v, %v", preview, err)
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
	if _, err = workspace.PreviewRecommendation(t.Context(), request, []tui.LLMChange{
		{FieldID: application.DeploymentCPUs.ID(), Value: "2"},
		{FieldID: application.DeploymentCPUs.ID(), Value: "3"},
	}); err == nil {
		t.Fatal("PreviewRecommendation(duplicate) succeeded")
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

func TestPublicLLMResultAndConfigurationErrorsRemainPrivacySafe(t *testing.T) {
	t.Parallel()
	result := publicLLMResult("opaque", llm.Result{
		RequestedModel: "requested", ReportedModel: "reported", ModelWarning: true,
		Choices: []llm.Recommendation{{
			Summary: "Use two CPUs", Changes: []llm.Change{{FieldID: "cpus", Value: "2"}},
		}},
	})
	if result.Token != "opaque" || len(result.Choices) != 1 || result.Choices[0].Changes[0].Value != "2" {
		t.Fatalf("publicLLMResult() = %#v", result)
	}
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

//nolint:cyclop,funlen,gocognit,paralleltest // The contract test temporarily replaces http.DefaultTransport.
func TestTUIAssistantRecommendsAndAcceptsWithPinnedCompatibleAdapter(t *testing.T) {
	originalTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })
	var providerFailure atomic.Bool
	var driftSource atomic.Bool
	var driftRuntime atomic.Bool
	var sourcePath string
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
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
		_, _ = io.WriteString(response, assistantCompletionBody("assistant-test-model"))
	}))
	t.Cleanup(server.Close)
	http.DefaultTransport = server.Client().Transport

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	sourcePath = filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	operations := &assistantOperationsFixture{snapshot: func(
		context.Context,
		application.Request,
	) (application.OperationSnapshot, error) {
		snapshot := validAssistantSnapshot()
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
	http.DefaultTransport = nil
	failingAssistant := defaultTUIAssistant(environment, repository, workspace, operations)
	if _, err = failingAssistant.Recommend(
		t.Context(), request, configuration.Identity, "Recommend a bounded CPU limit",
	); err == nil {
		t.Fatal("Recommend(provider construction failure) succeeded")
	}
	http.DefaultTransport = server.Client().Transport
	result, err := assistant.Recommend(
		t.Context(), request, configuration.Identity, "Recommend a bounded CPU limit",
	)
	if err != nil || len(result.Choices) != 1 {
		t.Fatalf("Recommend() = %#v, %v", result, err)
	}
	if _, err = workspace.PreviewRecommendation(t.Context(), request, result.Choices[0].Changes); err != nil {
		t.Fatalf("PreviewRecommendation() error = %v", err)
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
	if _, err = workspace.PreviewRecommendation(
		t.Context(), request, staleResult.Choices[0].Changes,
	); err != nil {
		t.Fatalf("PreviewRecommendation(before accept drift) error = %v", err)
	}
	driftRuntime.Store(true)
	err = assistant.Accept(t.Context(), staleResult.Token, 0)
	action, valid := errors.AsType[*tui.LLMActionError](err)
	if !valid || action.Code != tui.LLMContextStale || assistant.session != nil || len(assistant.pending) != 0 {
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
	}
	providerFailure.Store(false)
	if err = assistant.Accept(t.Context(), result.Token, 0); err == nil {
		t.Fatal("Accept(reused token) succeeded")
	}
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
	actionError, valid := errors.AsType[*tui.LLMActionError](err)
	if !valid || actionError.Code != tui.LLMConfigSaveUnknown || !configuration.Complete {
		t.Fatalf("Save(unknown) = %#v, %v", configuration, err)
	}
	unknownReload := newAssistant(t)
	unknownCtx, cancelUnknown := context.WithCancel(t.Context())
	unknownReload.configOps.openDirectory = func(*os.Root, string) (*os.File, error) {
		cancelUnknown()

		return nil, errTUIAssistantFixture
	}
	_, err = unknownReload.Save(unknownCtx, settings)
	actionError, valid = errors.AsType[*tui.LLMActionError](err)
	if !valid || actionError.Code != tui.LLMConfigSaveUnknown {
		t.Fatalf("Save(unknown reload failure) error = %v", err)
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
	assistant.pending["token"] = tuiAssistantPending{}
	assistant.closeSession()
	if assistant.sessionID != "" || len(assistant.pending) != 0 {
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
	if category := forbiddenQuestionCategory("x", map[string][]string{testLLMCredential: {"x", " "}}); category != "" {
		t.Fatalf("short forbidden value matched as %q", category)
	}

	codes := []llm.ErrorCode{
		llm.ErrorConfigInvalid, llm.ErrorQuestionInvalid, llm.ErrorForbiddenValue,
		llm.ErrorAuthentication, llm.ErrorRateLimited, llm.ErrorContextLimit,
		llm.ErrorRefused, llm.ErrorEmptyResponse, llm.ErrorTruncated,
		llm.ErrorInvalidResponse, llm.ErrorModelUnavailable, llm.ErrorTimeout,
		llm.ErrorCancelled, llm.ErrorContextStale, llm.ErrorProvider, llm.ErrorCode("unknown"),
	}
	for _, code := range codes {
		mapped := publicLLMActionError(&llm.ActionError{Code: code, Category: "category"})
		action, valid := errors.AsType[*tui.LLMActionError](mapped)
		if !valid || action.Category != "category" {
			t.Fatalf("publicLLMActionError(%s) = %v", code, mapped)
		}
	}
	if got := publicLLMActionError(errTUIAssistantFixture); got.Error() != string(tui.LLMProviderFailed) {
		t.Fatalf("private error = %v", got)
	}
	if got := publicLLMConfigError(errTUIAssistantFixture); got.Error() != string(tui.LLMConfigInvalid) {
		t.Fatalf("private config error = %v", got)
	}
}

func assistantCompletionBody(model string) string {
	return `{"id":"completion","object":"chat.completion","created":1,"model":"` + model +
		`","choices":[{"index":0,"message":{"role":"assistant","content":` +
		`"{\"summary\":\"Set CPUs\",\"changes\":[{\"field\":\"cpus\",` +
		`\"value\":\"2\",\"unset\":false,\"citation\":\"cpus\"}]}"},"finish_reason":"stop"}]}`
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
