// Package tui presents the application façade without opening runtime or
// journal resources itself.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/charmbracelet/x/term"
)

const eventQueueCapacity = 64

var errInvalidInput = errors.New("TUI input is invalid")

// CatalogState describes whether registered services can be listed.
type CatalogState string

const (
	// CatalogReady identifies a readable service registration.
	CatalogReady CatalogState = "ready"
	// CatalogMissing identifies a host without a registered repository.
	CatalogMissing CatalogState = "missing"
	// CatalogUnavailable identifies registration state that cannot be trusted.
	CatalogUnavailable CatalogState = "unavailable"
)

// SourceBlocker is a stable, privacy-safe reason that a source cannot open.
type SourceBlocker string

const (
	// BlockerNone identifies a source ready for application operations.
	BlockerNone SourceBlocker = ""
	// BlockerInvalid identifies a Compose source that could not pass validation.
	BlockerInvalid SourceBlocker = "compose_source_invalid"
	// BlockerNotFound identifies a source absent from a fresh inventory.
	BlockerNotFound SourceBlocker = "compose_source_not_found"
	// BlockerUnavailable identifies source state that cannot be read safely.
	BlockerUnavailable SourceBlocker = "compose_source_unavailable"
)

// SourceDiagnosticReason is a stable Compose validation category rendered by the TUI.
type SourceDiagnosticReason string

const (
	// DiagnosticYAMLSyntax identifies unreadable YAML syntax.
	DiagnosticYAMLSyntax SourceDiagnosticReason = "yaml_syntax_invalid"
	// DiagnosticYAMLStructure identifies an invalid YAML mapping or value shape.
	DiagnosticYAMLStructure SourceDiagnosticReason = "yaml_structure_invalid"
	// DiagnosticYAMLUnsupported identifies unsupported YAML features.
	DiagnosticYAMLUnsupported SourceDiagnosticReason = "yaml_feature_unsupported"
	// DiagnosticComposeValidation identifies input rejected by Compose validation.
	DiagnosticComposeValidation SourceDiagnosticReason = "compose_validation_failed"
)

// SourceDiagnostic is a privacy-safe projection of a Compose validation failure.
type SourceDiagnostic struct {
	File   string
	Reason SourceDiagnosticReason
	Line   int
	Column int
}

// Service is one registered inventory row. ID is opaque outside Catalog.
type Service struct {
	ID         string
	Location   string
	Project    string
	Name       string
	Runtime    string
	Blocker    SourceBlocker
	Diagnostic SourceDiagnostic
}

// CatalogSnapshot is one fresh registered-service inventory.
type CatalogSnapshot struct {
	State               CatalogState
	Services            []Service
	SuggestedRepository string
}

// RepositorySetupMode selects the owned onboarding path for a desired-state checkout.
type RepositorySetupMode uint8

const (
	// RepositorySetupCreateGitHub creates a private repository through gh.
	RepositorySetupCreateGitHub RepositorySetupMode = iota + 1
	// RepositorySetupExisting clones or reuses an existing Git repository.
	RepositorySetupExisting
)

// RepositorySetupRequest is one confirmed repository onboarding effect.
type RepositorySetupRequest struct {
	Mode     RepositorySetupMode
	Remote   string
	Checkout string
}

// RegistrationResult is one privacy-safe repository setup result.
type RegistrationResult struct {
	Snapshot CatalogSnapshot
	Blocker  SourceBlocker
}

// ServiceDraft is one generated, effect-free service candidate.
type ServiceDraft struct {
	Runtime      string
	Image        string
	Service      string
	ComposePath  string
	Preparation  string
	WarningCount int
	Recovered    bool
}

// StagedService is the exact Git index projection awaiting commit.
type StagedService struct {
	Diff          string
	ComposePath   string
	Preparation   string
	CommitMessage string
}

// ServiceCommitResult is one proven commit outcome or an explicit unsigned fallback request.
// ValidationUnavailable means the commit succeeded but no immutable apply request could be prepared.
// PreparationRequired means the committed service cannot be validated or applied in this TUI session.
type ServiceCommitResult struct {
	Request               application.Request
	NeedsUnsignedApproval bool
	Committed             bool
	ValidationUnavailable bool
	PreparationRequired   bool
}

// DeploymentFieldState is one editable deployment field and its current value.
// An unavailable field cannot be edited for the selected service.
type DeploymentFieldState struct {
	ID          string
	Value       string
	Present     bool
	AllowsUnset bool
	Available   bool
}

// DeploymentEditPreview is one validated in-memory Compose candidate.
type DeploymentEditPreview struct {
	ComposePath string
	FieldIDs    []string
	Restore     string
}

// StagedDeploymentEdit is the exact modified-file Git index projection.
type StagedDeploymentEdit struct {
	Diff          string
	ComposePath   string
	CommitMessage string
}

// DeploymentCommitResult is one proven deployment edit commit outcome.
type DeploymentCommitResult struct {
	Request               application.Request
	NeedsUnsignedApproval bool
	Committed             bool
	ValidationUnavailable bool
}

// DeploymentHistoryEntry is one first-parent commit that changed the selected Compose file.
type DeploymentHistoryEntry struct {
	Revision         string
	Subject          string
	SignaturePresent bool
}

// LLMConfiguration is the privacy-safe effective provider configuration.
type LLMConfiguration struct {
	Provider      string
	Model         string
	Endpoint      string
	Timeout       string
	Origin        string
	KeySource     string
	KeyConfigured bool
	Complete      bool
	Identity      string
	Warnings      []string
}

// LLMSettings is one TUI configuration draft. An empty APIKey preserves the
// current protected value; ClearAPIKey explicitly removes it from XDG config.
type LLMSettings struct {
	Provider    string
	Model       string
	Endpoint    string
	Timeout     string
	APIKey      string
	ClearAPIKey bool
}

// LLMChange is one locally validated field mutation in a provider choice.
type LLMChange struct {
	FieldID string
	Value   string
	Unset   bool
}

// LLMRecommendation is one provider choice that still requires user selection.
type LLMRecommendation struct {
	Summary string
	Changes []LLMChange
}

// LLMResult is one bounded completion result. Token is opaque to the TUI.
type LLMResult struct {
	Token          string
	RequestedModel string
	ReportedModel  string
	ModelWarning   bool
	Choices        []LLMRecommendation
}

// LLMActionCode is one privacy-safe assistant or configuration outcome.
type LLMActionCode string

// Stable LLM action codes returned through the Assistant role.
const (
	LLMConfigInvalid        LLMActionCode = "llm_config_invalid"
	LLMConfigPathInvalid    LLMActionCode = "llm_config_path_invalid"
	LLMConfigSaveStale      LLMActionCode = "config_save_stale"
	LLMConfigSaveUnknown    LLMActionCode = "config_save_outcome_unknown"
	LLMQuestionInvalid      LLMActionCode = "llm_question_invalid"
	LLMForbiddenValue       LLMActionCode = "llm_forbidden_value"
	LLMAuthenticationFailed LLMActionCode = "llm_authentication_failed"
	LLMRateLimited          LLMActionCode = "llm_rate_limited"
	LLMContextLimit         LLMActionCode = "llm_context_limit"
	LLMRefused              LLMActionCode = "llm_refused"
	LLMEmptyResponse        LLMActionCode = "llm_empty_response"
	LLMTruncated            LLMActionCode = "llm_truncated"
	LLMInvalidResponse      LLMActionCode = "llm_invalid_response"
	LLMModelUnavailable     LLMActionCode = "llm_model_unavailable"
	LLMTimeout              LLMActionCode = "llm_timeout"
	LLMCancelled            LLMActionCode = "llm_cancelled"
	LLMProviderFailed       LLMActionCode = "llm_provider_failed"
	LLMContextStale         LLMActionCode = "llm_context_stale"
)

// LLMActionError contains no provider response, dependency error, secret, or private value.
type LLMActionError struct {
	Code     LLMActionCode
	Category string
}

func (failure *LLMActionError) Error() string {
	if failure == nil {
		return "LLM action failed"
	}
	if failure.Category == "" {
		return string(failure.Code)
	}

	return string(failure.Code) + ": " + failure.Category
}

// Target is one validated service that can enter the operation façade.
type Target struct {
	Project string
	Service string
	Runtime string
	Request application.Request
}

// OpenResult is a typed, privacy-safe source lookup result.
type OpenResult struct {
	Targets    []Target
	Blocker    SourceBlocker
	Diagnostic SourceDiagnostic
}

// Catalog owns registered inventory and Compose source lookup for the TUI.
type Catalog interface {
	Snapshot(ctx context.Context) CatalogSnapshot
	OpenRegistered(ctx context.Context, id string) OpenResult
	OpenPath(ctx context.Context, path string) OpenResult
	Register(ctx context.Context, request RepositorySetupRequest) RegistrationResult
}

// ServiceWorkspace owns the generated file and Git transaction behind Add service.
type ServiceWorkspace interface {
	Preview(ctx context.Context, input string) (ServiceDraft, error)
	Stage(ctx context.Context) (StagedService, error)
	Commit(ctx context.Context, message string, unsigned bool) (ServiceCommitResult, error)
	Suspend(ctx context.Context) error
}

// DeploymentWorkspace owns manual Compose edits and their Git history transaction.
type DeploymentWorkspace interface {
	Fields(ctx context.Context, request application.Request) ([]DeploymentFieldState, error)
	Preview(
		ctx context.Context,
		request application.Request,
		fieldID string,
		value string,
		unset bool,
	) (DeploymentEditPreview, error)
	PreviewRestore(
		ctx context.Context,
		request application.Request,
		revision string,
	) (DeploymentEditPreview, error)
	Stage(ctx context.Context) (StagedDeploymentEdit, error)
	Commit(ctx context.Context, message string, unsigned bool) (DeploymentCommitResult, error)
	Discard(ctx context.Context) error
	History(ctx context.Context, request application.Request) ([]DeploymentHistoryEntry, error)
}

// DeploymentRecommendationWorkspace extends the manual editor with one
// multi-field LLM choice while retaining the same staged Git transaction.
type DeploymentRecommendationWorkspace interface {
	DeploymentWorkspace
	PreviewRecommendation(
		ctx context.Context,
		request application.Request,
		changes []LLMChange,
	) (DeploymentEditPreview, error)
}

// Assistant owns resolved configuration, provider networking, bounded chat,
// and stale-context validation outside the TUI model.
type Assistant interface {
	Configuration(ctx context.Context) (LLMConfiguration, error)
	Save(ctx context.Context, settings LLMSettings) (LLMConfiguration, error)
	Recommend(
		ctx context.Context,
		request application.Request,
		configurationIdentity string,
		question string,
	) (LLMResult, error)
	Accept(ctx context.Context, token string, choice int) error
	Close()
}

// Operations is the mutation façade consumed by the TUI.
type Operations interface {
	DryRun(ctx context.Context, request application.Request) (application.Plan, error)
	Apply(ctx context.Context, request application.Request) (application.Plan, error)
	Snapshot(ctx context.Context, request application.Request) (application.OperationSnapshot, error)
	Evidence(snapshot application.OperationSnapshot) (application.EvidenceBundle, error)
}

var _ Operations = (*application.ApplyFacade)(nil)

// IsTerminal reports whether one file descriptor is an interactive terminal.
func IsTerminal(descriptor uintptr) bool {
	return term.IsTerminal(descriptor)
}

// EventStream accepts bounded application events and exposes dropped-event
// counts to the application snapshot.
type EventStream struct {
	queue   chan application.Event
	dropped atomic.Uint64
}

// NewEventStream creates one process-local event stream for a TUI session.
func NewEventStream() *EventStream {
	return &EventStream{queue: make(chan application.Event, eventQueueCapacity)}
}

// TryPublish implements application.EventSink without blocking an operation.
func (stream *EventStream) TryPublish(event application.Event) bool {
	select {
	case stream.queue <- event:
		return true
	default:
		stream.dropped.Add(1)

		return false
	}
}

// DroppedEvents implements application.EventDropCounter.
func (stream *EventStream) DroppedEvents() uint64 {
	return stream.dropped.Load()
}

func (stream *EventStream) wait(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if ctx.Err() != nil {
			return contextDoneMsg{}
		}
		select {
		case event := <-stream.queue:
			return messageForEvent(ctx, event)
		case <-ctx.Done():
			return contextDoneMsg{}
		}
	}
}

//nolint:ireturn // Bubble Tea commands communicate through the tea.Msg extension contract.
func messageForEvent(ctx context.Context, event application.Event) tea.Msg {
	if ctx.Err() != nil {
		return contextDoneMsg{}
	}

	return eventMsg(event)
}

// Run opens one Bubble Tea session over an injected application façade.
func Run(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	catalog Catalog,
	workspace ServiceWorkspace,
	deployments DeploymentWorkspace,
	operations Operations,
	events *EventStream,
	options Options,
) error {
	return run(ctx, input, output, catalog, workspace, deployments, nil, operations, events, options)
}

// RunWithAssistant opens one session with the optional LLM capability enabled.
func RunWithAssistant(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	catalog Catalog,
	workspace ServiceWorkspace,
	deployments DeploymentWorkspace,
	assistant Assistant,
	operations Operations,
	events *EventStream,
	options Options,
) error {
	if assistant != nil {
		if _, valid := deployments.(DeploymentRecommendationWorkspace); !valid {
			return errInvalidInput
		}
	}

	return run(ctx, input, output, catalog, workspace, deployments, assistant, operations, events, options)
}

func run(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	catalog Catalog,
	workspace ServiceWorkspace,
	deployments DeploymentWorkspace,
	assistant Assistant,
	operations Operations,
	events *EventStream,
	options Options,
) error {
	if catalog == nil || workspace == nil || deployments == nil || operations == nil || events == nil {
		return errInvalidInput
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := newModelWithCapabilities(
		runCtx, catalog, workspace, deployments, assistant, operations, events, options,
	)
	_, err := tea.NewProgram(
		state,
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithWindowSize(defaultWidth, defaultHeight),
	).Run()
	cleanupCtx := context.WithoutCancel(ctx)
	cleanupErr := errors.Join(workspace.Suspend(cleanupCtx), deployments.Discard(cleanupCtx))
	if assistant != nil {
		assistant.Close()
	}
	if err != nil {
		return errors.Join(fmt.Errorf("run TUI: %w", err), cleanupErr)
	}
	if state.err != nil {
		return errors.Join(fmt.Errorf("run TUI operation: %w", state.err), cleanupErr)
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(fmt.Errorf("run TUI context: %w", err), cleanupErr)
	}

	return errors.Join(cleanupErr)
}
