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
type ServiceCommitResult struct {
	Request               application.Request
	NeedsUnsignedApproval bool
	Committed             bool
	ValidationUnavailable bool
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
	Register(ctx context.Context, path string) RegistrationResult
}

// ServiceWorkspace owns the generated file and Git transaction behind Add service.
type ServiceWorkspace interface {
	Preview(ctx context.Context, input string) (ServiceDraft, error)
	Stage(ctx context.Context) (StagedService, error)
	Commit(ctx context.Context, message string, unsigned bool) (ServiceCommitResult, error)
	Discard(ctx context.Context) error
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
	operations Operations,
	events *EventStream,
	options Options,
) error {
	if catalog == nil || workspace == nil || operations == nil || events == nil {
		return errInvalidInput
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := newModel(runCtx, catalog, workspace, operations, events, options)
	_, err := tea.NewProgram(
		state,
		tea.WithInput(input),
		tea.WithOutput(output),
	).Run()
	discardErr := workspace.Discard(context.WithoutCancel(ctx))
	if err != nil {
		return errors.Join(fmt.Errorf("run TUI: %w", err), discardErr)
	}
	if state.err != nil {
		return errors.Join(fmt.Errorf("run TUI operation: %w", state.err), discardErr)
	}
	if err = ctx.Err(); err != nil {
		return errors.Join(fmt.Errorf("run TUI context: %w", err), discardErr)
	}

	return errors.Join(discardErr)
}
