package application

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	operationOpenReader    = "open-reader"
	operationOpenRuntime   = "open-runtime"
	operationSelectRuntime = "select-runtime"
	operationOpenState     = "open-state"
	operationApply         = "apply"
	operationCloseReader   = "close-reader"
	operationCloseRuntime  = "close-runtime"
)

type operationRuntimeFixture struct {
	*testRuntime

	events *[]string
}

func (runtime *operationRuntimeFixture) ProbeImage(
	context.Context,
	domain.ImageIdentity,
) (ImageProbe, error) {
	return ImageProbe{}, errTestBoundary
}

func (runtime *operationRuntimeFixture) CloseIdleConnections() {
	*runtime.events = append(*runtime.events, operationCloseRuntime)
}

type operationReaderFixture struct {
	*testTransactions

	events   *[]string
	closeErr error
}

func (reader *operationReaderFixture) Close() error {
	*reader.events = append(*reader.events, operationCloseReader)

	return reader.closeErr
}

func TestApplyFacadeReturnsBoundedRepositoryInventory(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	scope, err := compose.NewRepositoryScope(
		filepath.Clean(t.TempDir()),
		"https://example.com/team/desired.git",
		"main",
	)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	records := []store.Transaction{
		{
			ID: store.TransactionID{1}, State: store.TransactionActive,
			SourceDigest:      domain.Hash([]byte("source one")),
			RepositoryVersion: scope.Version, RepositoryScopeDigest: scope.Digest,
			RepositoryLocationDigest: domain.Hash([]byte("services/one.yaml")), HasRepository: true,
		},
		{
			ID: store.TransactionID{2}, State: store.TransactionDegraded,
			SourceDigest:      domain.Hash([]byte("source two")),
			RepositoryVersion: scope.Version, RepositoryScopeDigest: scope.Digest,
			RepositoryLocationDigest: domain.Hash([]byte("services/two.yaml")), HasRepository: true,
		},
	}
	operation.transactions.repository = func(
		_ context.Context,
		gotScope domain.Digest,
	) ([]store.Transaction, error) {
		if gotScope != scope.Digest {
			t.Fatalf("inventory scope = %x", gotScope)
		}

		return records, nil
	}
	readerEvents := make([]string, 0, 1)
	facade := newOperationTestFacade(
		operation,
		&operationRuntimeFixture{testRuntime: operation.runtime, events: new([]string)},
		&operationReaderFixture{testTransactions: operation.transactions, events: &readerEvents},
	)

	got, err := facade.RepositoryInventory(t.Context(), scope)
	if err != nil {
		t.Fatalf("RepositoryInventory() error = %v", err)
	}
	want := []RepositoryTransaction{
		{
			ID: records[0].ID, State: records[0].State,
			Source: records[0].SourceDigest, Location: records[0].RepositoryLocationDigest,
		},
		{
			ID: records[1].ID, State: records[1].State,
			Source: records[1].SourceDigest, Location: records[1].RepositoryLocationDigest,
		},
	}
	if !slices.Equal(got, want) || !slices.Equal(readerEvents, []string{operationCloseReader}) {
		t.Fatalf("RepositoryInventory() = %#v, events %q", got, readerEvents)
	}
}

func TestApplyFacadeRejectsRepositoryInventoryOverflow(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	scope, err := compose.NewRepositoryScope(
		filepath.Clean(t.TempDir()),
		"https://example.com/team/desired.git",
		"main",
	)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	operation.transactions.repository = func(
		context.Context,
		domain.Digest,
	) ([]store.Transaction, error) {
		return make([]store.Transaction, maximumRepositoryInventory+1), nil
	}
	facade := newOperationTestFacade(
		operation,
		&operationRuntimeFixture{testRuntime: operation.runtime, events: new([]string)},
		&operationReaderFixture{testTransactions: operation.transactions, events: new([]string)},
	)

	_, err = facade.RepositoryInventory(t.Context(), scope)
	if !errors.Is(err, ErrRepositoryInventoryOverflow) {
		t.Fatalf("RepositoryInventory(overflow) error = %v", err)
	}
}

//nolint:cyclop,funlen // One table covers every malformed inventory field and resource failure boundary.
func TestApplyFacadeContainsRepositoryInventoryFailures(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	scope, err := compose.NewRepositoryScope(
		filepath.Clean(t.TempDir()),
		"https://example.com/team/desired.git",
		"main",
	)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	validRecord := store.Transaction{
		ID: store.TransactionID{1}, State: store.TransactionActive,
		SourceDigest:      domain.Hash([]byte("source")),
		RepositoryVersion: scope.Version, RepositoryScopeDigest: scope.Digest,
		RepositoryLocationDigest: domain.Hash([]byte("services/api.yaml")), HasRepository: true,
	}
	readerEvents := make([]string, 0, 1)
	reader := &operationReaderFixture{testTransactions: operation.transactions, events: &readerEvents}
	facade := newOperationTestFacade(
		operation,
		&operationRuntimeFixture{testRuntime: operation.runtime, events: new([]string)},
		reader,
	)

	if _, err = (*ApplyFacade)(nil).RepositoryInventory(t.Context(), scope); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("RepositoryInventory(nil facade) error = %v", err)
	}
	if _, err = (&ApplyFacade{}).RepositoryInventory(t.Context(), scope); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("RepositoryInventory(missing opener) error = %v", err)
	}
	if _, err = facade.RepositoryInventory(t.Context(), compose.RepositoryScope{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("RepositoryInventory(invalid scope) error = %v", err)
	}

	facade.openReader = func(context.Context) (OperationReader, error) { return nil, errTestBoundary }
	if _, err = facade.RepositoryInventory(t.Context(), scope); !errors.Is(err, errTestBoundary) {
		t.Fatalf("RepositoryInventory(open failure) error = %v", err)
	}
	facade.openReader = func(context.Context) (OperationReader, error) {
		//nolint:nilnil // This fixture verifies that the façade rejects a broken opener contract.
		return nil, nil
	}
	if _, err = facade.RepositoryInventory(t.Context(), scope); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("RepositoryInventory(nil reader) error = %v", err)
	}

	facade.openReader = func(context.Context) (OperationReader, error) { return reader, nil }
	operation.transactions.repository = func(context.Context, domain.Digest) ([]store.Transaction, error) {
		return nil, errTestBoundary
	}
	if _, err = facade.RepositoryInventory(t.Context(), scope); !errors.Is(err, errTestBoundary) {
		t.Fatalf("RepositoryInventory(read failure) error = %v", err)
	}
	operation.transactions.repository = func(context.Context, domain.Digest) ([]store.Transaction, error) {
		return []store.Transaction{validRecord}, nil
	}
	reader.closeErr = errTestBoundary
	if _, err = facade.RepositoryInventory(t.Context(), scope); !errors.Is(err, errTestBoundary) {
		t.Fatalf("RepositoryInventory(close failure) error = %v", err)
	}
	reader.closeErr = nil

	invalidRecords := make([]store.Transaction, 1, 7)
	record := validRecord
	record.SourceDigest = domain.Digest{}
	invalidRecords = append(invalidRecords, record)
	record = validRecord
	record.RepositoryLocationDigest = domain.Digest{}
	invalidRecords = append(invalidRecords, record)
	record = validRecord
	record.HasRepository = false
	invalidRecords = append(invalidRecords, record)
	record = validRecord
	record.RepositoryVersion++
	invalidRecords = append(invalidRecords, record)
	record = validRecord
	record.RepositoryScopeDigest = domain.Digest{}
	invalidRecords = append(invalidRecords, record)
	record = validRecord
	record.State = store.TransactionFailed
	invalidRecords = append(invalidRecords, record)
	for _, record := range invalidRecords {
		if _, err = repositoryInventory([]store.Transaction{record}, scope); !errors.Is(err, ErrConflictingState) {
			t.Fatalf("repositoryInventory(%#v) error = %v", record, err)
		}
	}
}

func TestApplyFacadeDryRunOwnsResourceLifetime(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	events := make([]string, 0, 10)
	runtime := &operationRuntimeFixture{testRuntime: operation.runtime, events: &events}
	reader := &operationReaderFixture{testTransactions: operation.transactions, events: &events}
	facade := NewApplyFacade(
		operation.service.images,
		bootstrapCredentials{},
		func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
			events = append(events, operationSelectRuntime)

			return func(context.Context) (OperationRuntime, error) {
				events = append(events, operationOpenRuntime)

				return runtime, nil
			}, nil
		},
		func(context.Context) (OperationReader, error) {
			events = append(events, operationOpenReader)

			return reader, nil
		},
		func(context.Context) (*store.Store, error) {
			t.Fatal("dry-run opened mutable state")

			return nil, errTestBoundary
		},
		nil,
	)

	plan, err := facade.DryRun(t.Context(), operation.request)
	if err != nil || plan.Kind != PlanBootstrap {
		t.Fatalf("DryRun() = %#v, %v", plan, err)
	}
	if len(events) < 5 || events[0] != operationSelectRuntime || events[1] != operationOpenReader ||
		events[2] != operationOpenRuntime ||
		events[len(events)-2] != operationCloseRuntime || events[len(events)-1] != operationCloseReader {
		t.Fatalf("resource events = %q", events)
	}
}

//nolint:funlen // The test keeps one mutation resource-ordering scenario together.
func TestApplyFacadeOwnsMutationResourceLifetime(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	events := make([]string, 0, 4)
	runtime := &operationRuntimeFixture{testRuntime: operation.runtime, events: &events}
	state := openMutationTestStore(t)
	facade := NewApplyFacade(
		operation.service.images,
		bootstrapCredentials{},
		func(kind domain.RuntimeKind) (OperationRuntimeFactory, error) {
			events = append(events, operationSelectRuntime)
			if kind != domain.RuntimeDocker {
				t.Fatalf("open runtime kind = %q", kind)
			}

			return func(context.Context) (OperationRuntime, error) {
				events = append(events, operationOpenRuntime)

				return runtime, nil
			}, nil
		},
		func(context.Context) (OperationReader, error) {
			t.Fatal("mutation opened read-only state")

			return nil, errTestBoundary
		},
		func(context.Context) (*store.Store, error) {
			events = append(events, operationOpenState)

			return state, nil
		},
		nil,
	)
	want := Plan{Kind: PlanUnchanged}
	facade.apply = func(
		context.Context,
		Request,
		*store.Store,
		OperationRuntime,
	) (Plan, error) {
		events = append(events, operationApply)

		return want, nil
	}

	got, err := facade.Apply(t.Context(), operation.request)
	if err != nil || got.Kind != want.Kind {
		t.Fatalf("Apply() = %#v, %v", got, err)
	}
	wantEvents := []string{
		operationSelectRuntime,
		operationOpenRuntime,
		operationOpenState,
		operationApply,
		operationCloseRuntime,
	}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("resource events = %q", events)
	}
	if closeErr := state.Close(); !errors.Is(closeErr, store.ErrUnavailable) {
		t.Fatalf("state remained open: %v", closeErr)
	}
}

//nolint:cyclop // The test checks matching and cleanup at each resource stage.
func TestApplyFacadeDryRunJoinsResourceFailures(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	runtimeEvents := make([]string, 0, 1)
	runtime := &operationRuntimeFixture{testRuntime: operation.runtime, events: &runtimeEvents}
	readerEvents := make([]string, 0, 1)
	reader := &operationReaderFixture{
		testTransactions: operation.transactions,
		events:           &readerEvents,
		closeErr:         store.ErrUnavailable,
	}
	facade := newOperationTestFacade(operation, runtime, reader)
	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return nil, errTestBoundary
	}

	_, err := facade.DryRun(t.Context(), operation.request)
	if !errors.Is(err, errTestBoundary) || len(readerEvents) != 0 || len(runtimeEvents) != 0 {
		t.Fatalf("DryRun(select runtime) = %v, reader %q, runtime %q", err, readerEvents, runtimeEvents)
	}

	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return func(context.Context) (OperationRuntime, error) {
			return nil, errTestBoundary
		}, nil
	}

	_, err = facade.DryRun(t.Context(), operation.request)
	if !errors.Is(err, errTestBoundary) || !errors.Is(err, store.ErrUnavailable) ||
		len(readerEvents) != 1 || len(runtimeEvents) != 0 {
		t.Fatalf("DryRun(open runtime) = %v, reader %q, runtime %q", err, readerEvents, runtimeEvents)
	}

	runtime.inspect = func(context.Context) (RuntimeEvidence, error) {
		return RuntimeEvidence{}, errTestBoundary
	}
	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return func(context.Context) (OperationRuntime, error) { return runtime, nil }, nil
	}
	readerEvents = readerEvents[:0]

	_, err = facade.DryRun(t.Context(), operation.request)
	if !errors.Is(err, errTestBoundary) || !errors.Is(err, store.ErrUnavailable) ||
		len(readerEvents) != 1 || len(runtimeEvents) != 1 {
		t.Fatalf("DryRun(run) = %v, reader %q, runtime %q", err, readerEvents, runtimeEvents)
	}
}

func TestApplyFacadeContainsInvalidDependenciesAndOpenFailures(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	valid := newOperationTestFacade(
		operation,
		&operationRuntimeFixture{testRuntime: operation.runtime, events: new([]string)},
		&operationReaderFixture{testTransactions: operation.transactions, events: new([]string)},
	)
	invalid := []*ApplyFacade{
		nil,
		{},
		{images: valid.images},
		{images: valid.images, authenticator: valid.authenticator},
		{
			images: valid.images, authenticator: valid.authenticator,
			selectRuntime: valid.selectRuntime,
		},
		{
			images: valid.images, authenticator: valid.authenticator,
			selectRuntime: valid.selectRuntime, openReader: valid.openReader,
		},
		{
			images: valid.images, authenticator: valid.authenticator,
			selectRuntime: valid.selectRuntime, openReader: valid.openReader, openState: valid.openState,
		},
	}
	for _, facade := range invalid {
		if _, err := facade.DryRun(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("DryRun(invalid) error = %v", err)
		}
		if _, err := facade.Apply(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Apply(invalid) error = %v", err)
		}
	}
	badRequest := operation.request
	badRequest.Source.Content = nil
	if _, err := valid.DryRun(t.Context(), badRequest); err == nil {
		t.Fatal("DryRun(invalid request) succeeded")
	}
	if _, err := valid.Apply(t.Context(), badRequest); err == nil {
		t.Fatal("Apply(invalid request) succeeded")
	}

	valid.openReader = func(context.Context) (OperationReader, error) {
		return nil, errTestBoundary
	}
	if _, err := valid.DryRun(t.Context(), operation.request); !errors.Is(err, errTestBoundary) {
		t.Fatalf("DryRun(open reader) error = %v", err)
	}
	valid.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return nil, errTestBoundary
	}
	if _, err := valid.Apply(t.Context(), operation.request); !errors.Is(err, errTestBoundary) {
		t.Fatalf("Apply(select runtime) error = %v", err)
	}
}

func TestApplyFacadeContainsRuntimeFactoryFailure(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	facade := newOperationTestFacade(
		operation,
		&operationRuntimeFixture{testRuntime: operation.runtime, events: new([]string)},
		&operationReaderFixture{testTransactions: operation.transactions, events: new([]string)},
	)
	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return func(context.Context) (OperationRuntime, error) {
			return nil, errTestBoundary
		}, nil
	}
	if _, err := facade.Apply(t.Context(), operation.request); !errors.Is(err, errTestBoundary) {
		t.Fatalf("Apply(open runtime) error = %v", err)
	}
}

func TestApplyFacadeRejectsNilOpenedResources(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	readerEvents := make([]string, 0, 1)
	runtimeEvents := make([]string, 0, 1)
	runtime := &operationRuntimeFixture{testRuntime: operation.runtime, events: &runtimeEvents}
	reader := &operationReaderFixture{testTransactions: operation.transactions, events: &readerEvents}
	facade := newOperationTestFacade(operation, runtime, reader)

	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		//nolint:nilnil // This fixture verifies that the façade rejects a broken selector contract.
		return nil, nil
	}
	if _, err := facade.DryRun(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("DryRun(nil runtime factory) error = %v", err)
	}
	if _, err := facade.Apply(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Apply(nil runtime factory) error = %v", err)
	}
	if _, err := facade.Snapshot(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Snapshot(nil runtime factory) error = %v", err)
	}

	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return func(context.Context) (OperationRuntime, error) {
			//nolint:nilnil // This fixture verifies that the façade rejects a broken opener contract.
			return nil, nil
		}, nil
	}
	if _, err := facade.DryRun(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) ||
		!slices.Equal(readerEvents, []string{operationCloseReader}) {
		t.Fatalf("DryRun(nil runtime) error = %v, reader events = %q", err, readerEvents)
	}
	if _, err := facade.Apply(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("Apply(nil runtime) error = %v", err)
	}

	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return func(context.Context) (OperationRuntime, error) { return runtime, nil }, nil
	}
	facade.openReader = func(context.Context) (OperationReader, error) {
		//nolint:nilnil // This fixture verifies that the façade rejects a broken opener contract.
		return nil, nil
	}
	if _, err := facade.DryRun(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("DryRun(nil reader) error = %v", err)
	}
	facade.openState = func(context.Context) (*store.Store, error) {
		//nolint:nilnil // This fixture verifies that the façade rejects a broken opener contract.
		return nil, nil
	}
	if _, err := facade.Apply(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) ||
		!slices.Equal(runtimeEvents, []string{operationCloseRuntime}) {
		t.Fatalf("Apply(nil state) error = %v, runtime events = %q", err, runtimeEvents)
	}
}

func TestApplyFacadeClosesRuntimeAfterStateAndMutationFailures(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	events := make([]string, 0, 1)
	runtime := &operationRuntimeFixture{testRuntime: operation.runtime, events: &events}
	facade := newOperationTestFacade(
		operation,
		runtime,
		&operationReaderFixture{testTransactions: operation.transactions, events: new([]string)},
	)
	facade.openState = func(context.Context) (*store.Store, error) {
		return nil, errTestBoundary
	}
	if _, err := facade.Apply(t.Context(), operation.request); !errors.Is(err, errTestBoundary) ||
		len(events) != 1 {
		t.Fatalf("Apply(open state) = %v, events %q", err, events)
	}

	state := openMutationTestStore(t)
	facade.openState = func(context.Context) (*store.Store, error) { return state, nil }
	facade.apply = func(
		context.Context,
		Request,
		*store.Store,
		OperationRuntime,
	) (Plan, error) {
		if err := state.Close(); err != nil {
			t.Fatalf("close state fixture: %v", err)
		}

		return Plan{}, errTestBoundary
	}
	events = events[:0]

	_, err := facade.Apply(t.Context(), operation.request)
	if !errors.Is(err, errTestBoundary) || !errors.Is(err, store.ErrUnavailable) || len(events) != 1 {
		t.Fatalf("Apply(run and close) = %v, events %q", err, events)
	}
}

func TestApplyFacadeSelectsComposeRuntime(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	kind, err := applyRuntimeKind(t.Context(), operation.request)
	if err != nil || kind != domain.RuntimeDocker {
		t.Fatalf("applyRuntimeKind() = %q, %v", kind, err)
	}

	invalidSource := operation.request
	invalidSource.Source.Content = nil
	if _, err = applyRuntimeKind(t.Context(), invalidSource); err == nil {
		t.Fatal("applyRuntimeKind(invalid source) succeeded")
	}
	missingService := operation.request
	missingService.Service = testOtherValue
	if _, err = applyRuntimeKind(t.Context(), missingService); err == nil {
		t.Fatal("applyRuntimeKind(missing service) succeeded")
	}
}

func TestApplyFacadeDelegatesMutationToObservedService(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	runtime := &operationRuntimeFixture{testRuntime: operation.runtime, events: new([]string)}
	facade := newOperationTestFacade(
		operation,
		runtime,
		&operationReaderFixture{testTransactions: operation.transactions, events: new([]string)},
	)
	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	if _, err := facade.applyService(
		t.Context(), operation.request, state, runtime,
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("applyService(incomplete runtime) error = %v", err)
	}
}

func newOperationTestFacade(
	operation testOperation,
	runtime OperationRuntime,
	reader OperationReader,
) *ApplyFacade {
	return NewApplyFacade(
		operation.service.images,
		bootstrapCredentials{},
		func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
			return func(context.Context) (OperationRuntime, error) { return runtime, nil }, nil
		},
		func(context.Context) (OperationReader, error) { return reader, nil },
		func(context.Context) (*store.Store, error) { return nil, errTestBoundary },
		nil,
	)
}
