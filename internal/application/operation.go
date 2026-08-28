package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
	"github.com/IceCodeNew/maniud/internal/store"
)

// OperationReader is one stable read-only journal snapshot.
type OperationReader interface {
	TransactionReader
	Close() error
}

// ApplyFacade owns per-call runtime and journal resources while delegating
// planning and mutation to the package-local service.
type ApplyFacade struct {
	images        ImageResolver
	authenticator credential.Provider
	selectRuntime func(domain.RuntimeKind) (OperationRuntimeFactory, error)
	openReader    func(context.Context) (OperationReader, error)
	openState     func(context.Context) (*store.Store, error)
	events        EventSink
	apply         func(context.Context, Request, *store.Store, OperationRuntime) (Plan, error)
	now           func() time.Time
}

// NewApplyFacade creates the application boundary used by apply consumers.
func NewApplyFacade(
	images ImageResolver,
	authenticator credential.Provider,
	selectRuntime func(domain.RuntimeKind) (OperationRuntimeFactory, error),
	openReader func(context.Context) (OperationReader, error),
	openState func(context.Context) (*store.Store, error),
	events EventSink,
) *ApplyFacade {
	facade := &ApplyFacade{
		images: images, authenticator: authenticator,
		selectRuntime: selectRuntime, openReader: openReader, openState: openState,
		events: events,
		now:    time.Now,
	}
	facade.apply = facade.applyService

	return facade
}

// DryRun prepares one apply and closes runtime and journal resources before it
// returns the plan.
func (facade *ApplyFacade) DryRun(ctx context.Context, request Request) (Plan, error) {
	var empty Plan
	if !facade.valid() {
		return empty, ErrInvalidRequest
	}

	runtimeKind, err := applyRuntimeKind(ctx, request)
	if err != nil {
		return empty, fmt.Errorf("select apply runtime: %w", err)
	}
	openRuntime, err := facade.runtimeFactory(runtimeKind)
	if err != nil {
		return empty, err
	}

	reader, err := facade.openReader(ctx)
	if err != nil {
		return empty, fmt.Errorf("open apply state: %w", err)
	}
	if reader == nil {
		return empty, ErrInvalidRequest
	}

	runtime, err := openRuntime(ctx)
	if err != nil {
		return empty, errors.Join(fmt.Errorf("open apply runtime: %w", err), reader.Close())
	}
	if runtime == nil {
		return empty, errors.Join(ErrInvalidRequest, reader.Close())
	}

	plan, runErr := newService(facade.images, runtime, reader, facade.events).DryRun(ctx, request)
	runtime.CloseIdleConnections()

	closeErr := reader.Close()
	if runErr != nil || closeErr != nil {
		return empty, errors.Join(runErr, closeErr)
	}

	return plan, nil
}

// Apply executes one mutation and closes runtime and journal resources before
// it returns the plan.
func (facade *ApplyFacade) Apply(ctx context.Context, request Request) (Plan, error) {
	var empty Plan
	if !facade.valid() {
		return empty, ErrInvalidRequest
	}

	runtimeKind, err := applyRuntimeKind(ctx, request)
	if err != nil {
		return empty, fmt.Errorf("select apply runtime: %w", err)
	}
	openRuntime, err := facade.runtimeFactory(runtimeKind)
	if err != nil {
		return empty, err
	}

	runtime, err := openRuntime(ctx)
	if err != nil {
		return empty, fmt.Errorf("open apply runtime: %w", err)
	}
	if runtime == nil {
		return empty, ErrInvalidRequest
	}

	state, err := facade.openState(ctx)
	if err != nil {
		runtime.CloseIdleConnections()

		return empty, fmt.Errorf("open apply state: %w", err)
	}
	if state == nil {
		runtime.CloseIdleConnections()

		return empty, ErrInvalidRequest
	}

	plan, runErr := facade.apply(ctx, request, state, runtime)
	runtime.CloseIdleConnections()

	closeErr := state.Close()
	if runErr != nil || closeErr != nil {
		return empty, errors.Join(runErr, closeErr)
	}

	return plan, nil
}

func (facade *ApplyFacade) valid() bool {
	return facade != nil && facade.images != nil && facade.authenticator != nil &&
		facade.selectRuntime != nil && facade.openReader != nil && facade.openState != nil &&
		facade.apply != nil
}

func (facade *ApplyFacade) runtimeFactory(kind domain.RuntimeKind) (OperationRuntimeFactory, error) {
	openRuntime, err := facade.selectRuntime(kind)
	if err != nil {
		return nil, fmt.Errorf("select compiled runtime: %w", err)
	}
	if openRuntime == nil {
		return nil, ErrInvalidRequest
	}

	return openRuntime, nil
}

func (facade *ApplyFacade) applyService(
	ctx context.Context,
	request Request,
	state *store.Store,
	runtime OperationRuntime,
) (Plan, error) {
	return newService(facade.images, runtime, state, facade.events).Apply(
		ctx,
		request,
		state,
		facade.authenticator,
	)
}

func applyRuntimeKind(ctx context.Context, request Request) (domain.RuntimeKind, error) {
	project, err := compose.Load(ctx, request.Source)
	if err != nil {
		return "", fmt.Errorf("load Compose runtime metadata: %w", err)
	}
	runtimeKind, err := project.Runtime(request.Service)
	if err != nil {
		return "", fmt.Errorf("select Compose service runtime: %w", err)
	}

	return runtimeKind, nil
}
