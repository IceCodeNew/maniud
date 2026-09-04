package application

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	snapshotCloseReader     = "snapshot-close-reader"
	snapshotCheckEvent      = "check"
	snapshotInspectEvent    = "inspect"
	snapshotObserveEvent    = "observe"
	snapshotOpenReader      = "snapshot-open-reader"
	snapshotOpenRuntime     = "snapshot-open-runtime"
	snapshotResolveEvent    = "resolve"
	snapshotTransactionCase = "transaction"
)

type snapshotEventSink struct {
	dropped uint64
	panic   bool
}

func (*snapshotEventSink) TryPublish(Event) bool {
	return true
}

func (sink *snapshotEventSink) DroppedEvents() uint64 {
	if sink.panic {
		panic("drop counter failure")
	}

	return sink.dropped
}

type snapshotReader struct {
	*testTransactions

	events   *[]string
	closeErr error
}

func (reader *snapshotReader) Close() error {
	*reader.events = append(*reader.events, snapshotCloseReader)

	return reader.closeErr
}

//nolint:cyclop // One stable capture assertion verifies the complete correlated snapshot contract.
func TestApplyFacadeCapturesStableSnapshot(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	sink := &snapshotEventSink{dropped: 7}
	facade := snapshotTestFacade(
		t, operation, operation.events, sink,
		operation.transactions, operation.transactions, operation.transactions, operation.transactions,
	)
	wantTime := time.Date(2026, time.August, 25, 12, 0, 0, 0, time.FixedZone("test", 3600))
	facade.now = func() time.Time { return wantTime }

	snapshot, err := facade.Snapshot(t.Context(), operation.request)
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if snapshot.CapturedAt != wantTime.UTC() || snapshot.Plan.Kind != PlanBootstrap ||
		snapshot.Runtime != testExecutionEvidence() || snapshot.HasTransaction || snapshot.HasApplied ||
		snapshot.Transaction != (SnapshotTransaction{}) || snapshot.Applied != (SnapshotAppliedService{}) ||
		len(snapshot.Actions) != 0 || snapshot.DroppedEvents != sink.dropped {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}

	wantEvents := []string{
		snapshotOpenReader, eventJournal, eventApplied, snapshotCloseReader,
		snapshotOpenRuntime, snapshotInspectEvent, snapshotResolveEvent, snapshotCheckEvent, snapshotObserveEvent,
		snapshotOpenReader, eventJournal, eventApplied, snapshotCloseReader,
		operationCloseRuntime,
	}
	if !slices.Equal(*operation.events, wantEvents) {
		t.Fatalf("snapshot events = %q, want %q", *operation.events, wantEvents)
	}
}

type snapshotCapability interface {
	Snapshot(ctx context.Context, request Request) (OperationSnapshot, error)
}

var _ snapshotCapability = (*ApplyFacade)(nil)

func TestApplyFacadeRetriesOneJournalDrift(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	desired, err := operation.service.prepareDesired(t.Context(), operation.request)
	if err != nil {
		t.Fatalf("prepare snapshot fixture: %v", err)
	}
	transaction := exactTransaction(prepareApplyEvidence(
		desired, missingObservation(), store.Transaction{}, false, store.AppliedService{}, false, time.Time{},
	), store.TransactionActive)
	intent := action(transaction, 1, store.ActionStateIntent)
	completed := action(transaction, 1, store.ActionStateCompleted)
	readers := []*testTransactions{
		snapshotTransactions(transaction, []store.Action{intent}),
		snapshotTransactions(transaction, []store.Action{completed}),
		snapshotTransactions(transaction, []store.Action{completed}),
		snapshotTransactions(transaction, []store.Action{completed}),
	}
	events := make([]string, 0, 32)
	facade := snapshotTestFacade(t, operation, &events, nil, readers...)

	snapshot, err := facade.Snapshot(t.Context(), operation.request)
	if err != nil || !snapshot.HasTransaction || len(snapshot.Actions) != 1 ||
		snapshot.Actions[0].State != string(store.ActionStateCompleted) ||
		countSnapshotEvent(events, snapshotOpenRuntime) != maximumSnapshotAttempts {
		t.Fatalf("Snapshot(single drift) = %#v, %v; events %q", snapshot, err, events)
	}
}

func TestApplyFacadeRejectsRepeatedJournalDrift(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	desired, err := operation.service.prepareDesired(t.Context(), operation.request)
	if err != nil {
		t.Fatalf("prepare snapshot fixture: %v", err)
	}
	transaction := exactTransaction(prepareApplyEvidence(
		desired, missingObservation(), store.Transaction{}, false, store.AppliedService{}, false, time.Time{},
	), store.TransactionActive)
	intent := action(transaction, 1, store.ActionStateIntent)
	completed := action(transaction, 1, store.ActionStateCompleted)
	events := make([]string, 0, 32)
	facade := snapshotTestFacade(t, operation, &events, nil,
		snapshotTransactions(transaction, []store.Action{intent}),
		snapshotTransactions(transaction, []store.Action{completed}),
		snapshotTransactions(transaction, []store.Action{intent}),
		snapshotTransactions(transaction, []store.Action{completed}),
	)

	if snapshot, err := facade.Snapshot(t.Context(), operation.request); !snapshot.CapturedAt.IsZero() ||
		snapshot.Plan.Kind != "" || !errors.Is(err, ErrSnapshotStale) ||
		countSnapshotEvent(events, operationCloseRuntime) != maximumSnapshotAttempts {
		t.Fatalf("Snapshot(repeated drift) = %#v, %v; events %q", snapshot, err, events)
	}
}

//nolint:funlen // The table keeps every external snapshot boundary failure visible in one audit.
func TestApplyFacadeSnapshotContainsBoundaryFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*ApplyFacade, *operationRuntimeFixture)
		want   error
	}{
		{
			name: "open state",
			mutate: func(facade *ApplyFacade, _ *operationRuntimeFixture) {
				facade.openReader = func(context.Context) (OperationReader, error) {
					return nil, errTestBoundary
				}
			},
			want: errTestBoundary,
		},
		{
			name: "nil state",
			mutate: func(facade *ApplyFacade, _ *operationRuntimeFixture) {
				facade.openReader = func(context.Context) (OperationReader, error) {
					return nil, nil //nolint:nilnil // Exercise a malformed successful reader boundary.
				}
			},
			want: ErrInvalidRequest,
		},
		{
			name: "open runtime",
			mutate: func(facade *ApplyFacade, _ *operationRuntimeFixture) {
				facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
					return func(context.Context) (OperationRuntime, error) {
						return nil, errTestBoundary
					}, nil
				}
			},
			want: errTestBoundary,
		},
		{
			name: "nil runtime",
			mutate: func(facade *ApplyFacade, _ *operationRuntimeFixture) {
				facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
					return func(context.Context) (OperationRuntime, error) {
						return nil, nil //nolint:nilnil // Exercise a malformed successful runtime boundary.
					}, nil
				}
			},
			want: ErrInvalidRequest,
		},
		{
			name: "inspect runtime",
			mutate: func(_ *ApplyFacade, runtime *operationRuntimeFixture) {
				runtime.inspect = func(context.Context) (RuntimeEvidence, error) {
					return RuntimeEvidence{}, errTestBoundary
				}
			},
			want: errTestBoundary,
		},
		{
			name: "observe runtime",
			mutate: func(_ *ApplyFacade, runtime *operationRuntimeFixture) {
				runtime.observe = func(context.Context, domain.DesiredWorkload) (WorkloadObservation, error) {
					return WorkloadObservation{}, errTestBoundary
				}
			},
			want: errTestBoundary,
		},
		{
			name: "invalid observation",
			mutate: func(_ *ApplyFacade, runtime *operationRuntimeFixture) {
				runtime.observe = func(context.Context, domain.DesiredWorkload) (WorkloadObservation, error) {
					return WorkloadObservation{}, nil
				}
			},
			want: ErrConflictingState,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operation := newTestOperation(t)
			events := make([]string, 0, 16)
			runtime := &operationRuntimeFixture{testRuntime: operation.runtime, events: &events}
			facade := snapshotTestFacadeWithRuntime(
				t, operation, runtime, &events, operation.transactions, operation.transactions,
			)
			test.mutate(facade, runtime)

			_, err := facade.Snapshot(t.Context(), operation.request)
			if !errors.Is(err, test.want) {
				t.Fatalf("Snapshot() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestApplyFacadeSnapshotContainsSecondReadAndClassificationFailures(t *testing.T) {
	t.Parallel()

	t.Run("second read", func(t *testing.T) {
		t.Parallel()

		operation := newTestOperation(t)
		facade := snapshotTestFacade(
			t, operation, new([]string), nil, operation.transactions, operation.transactions,
		)
		openReader := facade.openReader
		calls := 0
		facade.openReader = func(ctx context.Context) (OperationReader, error) {
			calls++
			if calls == 2 {
				return nil, errTestBoundary
			}

			return openReader(ctx)
		}

		if _, err := facade.Snapshot(t.Context(), operation.request); !errors.Is(err, errTestBoundary) {
			t.Fatalf("Snapshot(second read) error = %v", err)
		}
	})

	t.Run("classification", func(t *testing.T) {
		t.Parallel()

		operation := newTestOperation(t)
		invalid := store.Transaction{
			ID: store.TransactionID{1}, Kind: store.TransactionBootstrap, State: store.TransactionActive,
			Runtime: domain.RuntimePodman, SourceDigest: domain.Hash([]byte("source")),
			EffectiveDigest: domain.Hash([]byte("desired")), ExecutionDigest: domain.Hash([]byte("execution")),
		}
		reader := snapshotTransactions(invalid, nil)
		facade := snapshotTestFacade(t, operation, new([]string), nil, reader, reader)

		if _, err := facade.Snapshot(t.Context(), operation.request); !errors.Is(err, ErrConflictingState) {
			t.Fatalf("Snapshot(classification) error = %v", err)
		}
	})
}

func TestApplyFacadeSnapshotRejectsInvalidFacadeAndSelection(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	valid := snapshotTestFacade(t, operation, new([]string), nil, operation.transactions, operation.transactions)
	invalid := []*ApplyFacade{
		nil,
		{},
		{images: valid.images},
		{images: valid.images, selectRuntime: valid.selectRuntime},
		{images: valid.images, selectRuntime: valid.selectRuntime, openReader: valid.openReader},
	}
	for _, facade := range invalid {
		if _, err := facade.Snapshot(t.Context(), operation.request); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("Snapshot(invalid facade) error = %v", err)
		}
	}

	badSource := operation.request
	badSource.Source.Content = nil
	if _, err := valid.Snapshot(t.Context(), badSource); err == nil {
		t.Fatal("Snapshot(invalid source) succeeded")
	}
	missingService := operation.request
	missingService.Service = testOtherValue
	if _, err := valid.Snapshot(t.Context(), missingService); err == nil {
		t.Fatal("Snapshot(missing service) succeeded")
	}
	valid.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return nil, errTestBoundary
	}
	if _, err := valid.Snapshot(t.Context(), operation.request); !errors.Is(err, errTestBoundary) {
		t.Fatalf("Snapshot(select runtime) error = %v", err)
	}
}

func TestReadSnapshotJournalContainsReadAndCloseFailures(t *testing.T) {
	t.Parallel()

	base := newTestTransactions(new([]string))
	tests := []struct {
		name   string
		mutate func(*testTransactions)
	}{
		{
			name: snapshotTransactionCase,
			mutate: func(reader *testTransactions) {
				reader.unresolved = func(context.Context, string, string) (store.Transaction, bool, error) {
					return store.Transaction{}, false, errTestBoundary
				}
			},
		},
		{
			name: "applied",
			mutate: func(reader *testTransactions) {
				reader.applied = func(context.Context, string, string) (store.AppliedService, bool, error) {
					return store.AppliedService{}, false, errTestBoundary
				}
			},
		},
		{
			name: "actions",
			mutate: func(reader *testTransactions) {
				reader.unresolved = func(context.Context, string, string) (store.Transaction, bool, error) {
					return store.Transaction{ID: store.TransactionID{1}}, true, nil
				}
				reader.actions = func(context.Context, store.TransactionID) ([]store.Action, error) {
					return nil, errTestBoundary
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			reader := *newTestTransactions(new([]string))
			test.mutate(&reader)
			_, err := readSnapshotJournal(t.Context(), &reader, testProjectName, testServiceName)
			if !errors.Is(err, errTestBoundary) {
				t.Fatalf("readSnapshotJournal() error = %v", err)
			}
		})
	}

	events := make([]string, 0, 1)
	facade := &ApplyFacade{
		openReader: func(context.Context) (OperationReader, error) {
			return &snapshotReader{testTransactions: base, events: &events, closeErr: errTestBoundary}, nil
		},
	}
	_, err := facade.readSnapshotJournal(t.Context(), testProjectName, testServiceName)
	if !errors.Is(err, errTestBoundary) || !slices.Equal(events, []string{snapshotCloseReader}) {
		t.Fatalf("readSnapshotJournal(close) error = %v, events %q", err, events)
	}
}

func TestSnapshotJournalEqualityUsesProjectedActionValues(t *testing.T) {
	t.Parallel()

	digest := domain.Hash([]byte("postcondition"))
	actionValue := store.Action{
		TransactionID: store.TransactionID{1}, Sequence: 1, Kind: workloadCreateActionKind,
		State: store.ActionStateCompleted, IntentDigest: domain.Hash([]byte("intent")),
		PostconditionDigest: &digest,
	}
	base := snapshotJournal{
		transaction: store.Transaction{ID: store.TransactionID{1}}, hasTransaction: true,
		applied: store.AppliedService{TransactionID: store.TransactionID{1}}, hasApplied: true,
		actions: []store.Action{actionValue},
	}
	copyValue := digest
	equal := base
	equal.actions = []store.Action{actionValue}
	equal.actions[0].PostconditionDigest = &copyValue
	if !sameSnapshotJournal(base, equal) {
		t.Fatal("equal projected journal values drifted")
	}

	tests := []struct {
		name   string
		mutate func(*snapshotJournal)
	}{
		{name: snapshotTransactionCase, mutate: func(value *snapshotJournal) { value.transaction.ID[0]++ }},
		{name: "transaction presence", mutate: func(value *snapshotJournal) { value.hasTransaction = false }},
		{name: "applied", mutate: func(value *snapshotJournal) { value.applied.TransactionID[0]++ }},
		{name: "applied presence", mutate: func(value *snapshotJournal) { value.hasApplied = false }},
		{name: "action length", mutate: func(value *snapshotJournal) { value.actions = nil }},
		{name: "action value", mutate: func(value *snapshotJournal) { value.actions[0].Sequence++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			changed := base
			changed.actions = slices.Clone(base.actions)
			test.mutate(&changed)
			if sameSnapshotJournal(base, changed) {
				t.Fatal("changed journal compared equal")
			}
		})
	}
}

func TestDroppedEventsContainsUnsupportedAndPanickingSinks(t *testing.T) {
	t.Parallel()

	if droppedEvents(nil) != 0 || droppedEvents(eventSinkFunc(func(Event) bool { return true })) != 0 {
		t.Fatal("sink without counter reported dropped events")
	}
	if droppedEvents(&snapshotEventSink{dropped: 9}) != 9 {
		t.Fatal("counter did not report dropped events")
	}
	if droppedEvents(&snapshotEventSink{panic: true}) != 0 {
		t.Fatal("panicking counter changed snapshot result")
	}
}

func TestOperationSnapshotProjectsUpgradeAndAppliedIdentities(t *testing.T) {
	t.Parallel()

	digest := domain.Hash([]byte("identity"))
	transaction := store.Transaction{
		ID: store.TransactionID{1}, Kind: store.TransactionUpgrade, State: store.TransactionDegraded,
		Runtime: domain.RuntimeDocker, SourceDigest: digest, EffectiveDigest: digest, ExecutionDigest: digest,
		BaseTransactionID: store.TransactionID{2}, HasBaseTransaction: true,
	}
	applied := store.AppliedService{
		TransactionID: store.TransactionID{2}, Runtime: domain.RuntimeDocker,
		SourceDigest: digest, EffectiveDigest: digest, ExecutionDigest: digest,
		ConfigurationDigest: digest, StorageDigest: digest, ReferenceDigest: digest,
		PlatformManifestDigest: digest, ImageConfigDigest: digest,
	}
	facade := &ApplyFacade{}
	capturedAt := time.Unix(1, 0).UTC()
	snapshot := operationSnapshot(facade, capturedAt, Preparation{
		Plan: Plan{Kind: PlanRestore}, Execution: testExecutionEvidence(),
		Transaction: transaction, HasTransaction: true, Applied: applied, HasApplied: true,
	})
	if snapshot.CapturedAt != capturedAt ||
		snapshot.Transaction.BaseTransaction != transaction.BaseTransactionID.String() ||
		snapshot.Applied.Transaction != applied.TransactionID.String() ||
		snapshot.Applied.ImageConfig != digest.String() {
		t.Fatalf("operationSnapshot() = %#v", snapshot)
	}
}

//nolint:funlen // One table exposes the complete transaction/plan projection matrix.
func TestHealthResolutionForSnapshotProjectsApplicationOwnedDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		preparation      Preparation
		action           HealthResolutionAction
		restoresPrevious bool
	}{
		{name: "no transaction"},
		{
			name: "pending adoption", action: HealthResolutionCancelAdoption,
			preparation: Preparation{
				HasTransaction: true, Transaction: store.Transaction{Kind: store.TransactionAdopt},
				Plan: Plan{Health: HealthConvergencePending},
			},
		},
		{
			name: "settled adoption",
			preparation: Preparation{
				HasTransaction: true, Transaction: store.Transaction{Kind: store.TransactionAdopt},
			},
		},
		{
			name: "degraded bootstrap", action: HealthResolutionRollback,
			preparation: Preparation{
				HasTransaction: true,
				Transaction: store.Transaction{
					Kind: store.TransactionBootstrap, State: store.TransactionHealthDegraded,
				},
				Plan: Plan{Health: HealthConvergenceDegraded},
			},
		},
		{
			name: "degraded upgrade", action: HealthResolutionRollback, restoresPrevious: true,
			preparation: Preparation{
				HasTransaction: true,
				Transaction: store.Transaction{
					Kind: store.TransactionUpgrade, State: store.TransactionHealthDegraded,
				},
				Plan: Plan{Health: HealthConvergenceDegraded},
			},
		},
		{
			name: "stopped predecessor", action: HealthResolutionRetryRestoreStart,
			preparation: Preparation{
				HasTransaction: true,
				Transaction: store.Transaction{
					Kind: store.TransactionUpgrade, State: store.TransactionDegraded,
				},
				Plan: Plan{
					Kind: PlanRestore, Health: HealthConvergenceDegraded,
					Observation: WorkloadObservation{
						State: WorkloadObservationPresent, Lifecycle: WorkloadLifecycleExited,
					},
				},
			},
		},
		{
			name: "running predecessor",
			preparation: Preparation{
				HasTransaction: true,
				Transaction: store.Transaction{
					Kind: store.TransactionUpgrade, State: store.TransactionDegraded,
				},
				Plan: Plan{
					Kind: PlanRestore, Health: HealthConvergenceDegraded,
					Observation: WorkloadObservation{
						State: WorkloadObservationPresent, Lifecycle: WorkloadLifecycleRunning,
					},
				},
			},
		},
		{
			name: "unknown transaction",
			preparation: Preparation{
				HasTransaction: true, Transaction: store.Transaction{Kind: store.TransactionKind("unknown")},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			action, restoresPrevious := healthResolutionForSnapshot(test.preparation)
			if action != test.action || restoresPrevious != test.restoresPrevious {
				t.Fatalf("healthResolutionForSnapshot() = %q, %t", action, restoresPrevious)
			}
		})
	}
}

func snapshotTestFacade(
	t *testing.T,
	operation testOperation,
	events *[]string,
	sink EventSink,
	readers ...*testTransactions,
) *ApplyFacade {
	t.Helper()

	runtime := &operationRuntimeFixture{testRuntime: operation.runtime, events: events}

	return snapshotTestFacadeWithRuntime(t, operation, runtime, events, readers...).withEvents(sink)
}

func snapshotTestFacadeWithRuntime(
	t *testing.T,
	operation testOperation,
	runtime OperationRuntime,
	events *[]string,
	readers ...*testTransactions,
) *ApplyFacade {
	t.Helper()

	readerIndex := 0
	facade := NewApplyFacade(
		operation.service.images,
		bootstrapCredentials{},
		func(kind domain.RuntimeKind) (OperationRuntimeFactory, error) {
			if kind != domain.RuntimeDocker {
				return nil, errTestBoundary
			}

			return func(context.Context) (OperationRuntime, error) {
				*events = append(*events, snapshotOpenRuntime)

				return runtime, nil
			}, nil
		},
		func(context.Context) (OperationReader, error) {
			*events = append(*events, snapshotOpenReader)
			if readerIndex >= len(readers) {
				return nil, errTestBoundary
			}
			reader := &snapshotReader{testTransactions: readers[readerIndex], events: events}
			readerIndex++

			return reader, nil
		},
		func(context.Context) (*store.Store, error) { return nil, errTestBoundary },
		nil,
	)
	facade.now = func() time.Time { return time.Unix(1, 0) }

	return facade
}

func (facade *ApplyFacade) withEvents(events EventSink) *ApplyFacade {
	facade.events = events

	return facade
}

func snapshotTransactions(transaction store.Transaction, actions []store.Action) *testTransactions {
	return &testTransactions{
		unresolved: func(context.Context, string, string) (store.Transaction, bool, error) {
			return transaction, true, nil
		},
		applied: func(context.Context, string, string) (store.AppliedService, bool, error) {
			return store.AppliedService{}, false, nil
		},
		actions: func(context.Context, store.TransactionID) ([]store.Action, error) {
			return slices.Clone(actions), nil
		},
	}
}

func countSnapshotEvent(events []string, target string) int {
	count := 0
	for _, event := range events {
		if event == target {
			count++
		}
	}

	return count
}
