package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const maximumSnapshotAttempts = 2

// ErrSnapshotStale reports durable identity drift across both bounded capture
// attempts.
var ErrSnapshotStale = errors.New("application snapshot is stale")

// SnapshotTransaction is the bounded durable transaction projection exposed
// to read-only consumers. Digest strings remain opaque identities.
type SnapshotTransaction struct {
	ID              string
	Kind            string
	State           string
	Runtime         domain.RuntimeKind
	Source          string
	Desired         string
	Execution       string
	BaseTransaction string
}

// SnapshotAppliedService is the bounded durable baseline projection exposed
// to read-only consumers.
type SnapshotAppliedService struct {
	Transaction   string
	Runtime       domain.RuntimeKind
	Source        string
	Desired       string
	Execution     string
	Configuration string
	Storage       string
	Reference     string
	Manifest      string
	ImageConfig   string
}

// SnapshotAction is one durable effect boundary. Intent and postcondition are
// opaque digest identities.
type SnapshotAction struct {
	Sequence      int64
	Kind          string
	State         string
	Intent        string
	Postcondition string
}

// OperationSnapshot correlates one typed runtime observation with durable
// state read immediately before and after that observation.
type OperationSnapshot struct {
	CapturedAt     time.Time
	Plan           Plan
	Runtime        RuntimeEvidence
	Transaction    SnapshotTransaction
	HasTransaction bool
	Applied        SnapshotAppliedService
	HasApplied     bool
	Actions        []SnapshotAction
	DroppedEvents  uint64
}

type snapshotJournal struct {
	transaction    store.Transaction
	hasTransaction bool
	applied        store.AppliedService
	hasApplied     bool
	actions        []store.Action
}

// Snapshot captures one correlated, read-only application view. A single
// durable drift retries the full capture once.
func (facade *ApplyFacade) Snapshot(ctx context.Context, request Request) (OperationSnapshot, error) {
	var empty OperationSnapshot
	if !facade.validSnapshot() {
		return empty, ErrInvalidRequest
	}

	project, err := compose.Load(ctx, request.Source)
	if err != nil {
		return empty, fmt.Errorf("load snapshot source: %w", err)
	}
	runtimeKind, err := project.Runtime(request.Service)
	if err != nil {
		return empty, fmt.Errorf("select snapshot service: %w", err)
	}
	openRuntime, err := facade.selectRuntime(runtimeKind)
	if err != nil {
		return empty, fmt.Errorf("select compiled runtime: %w", err)
	}

	for range maximumSnapshotAttempts {
		snapshot, stable, captureErr := facade.captureSnapshot(
			ctx,
			request,
			project.Name(),
			openRuntime,
		)
		if captureErr != nil {
			return empty, captureErr
		}
		if stable {
			return snapshot, nil
		}
	}

	return empty, ErrSnapshotStale
}

func (facade *ApplyFacade) validSnapshot() bool {
	return facade != nil && facade.images != nil && facade.selectRuntime != nil &&
		facade.openReader != nil && facade.now != nil
}

func (facade *ApplyFacade) captureSnapshot(
	ctx context.Context,
	request Request,
	project string,
	openRuntime OperationRuntimeFactory,
) (OperationSnapshot, bool, error) {
	var empty OperationSnapshot

	before, err := facade.readSnapshotJournal(ctx, project, request.Service)
	if err != nil {
		return empty, false, err
	}

	runtime, err := openRuntime(ctx)
	if err != nil {
		return empty, false, fmt.Errorf("open snapshot runtime: %w", err)
	}
	if runtime == nil {
		return empty, false, ErrInvalidRequest
	}
	defer runtime.CloseIdleConnections()

	service := &service{images: facade.images, runtime: runtime}
	desired, err := service.prepareDesired(ctx, request)
	if err != nil {
		return empty, false, err
	}
	observation, err := runtime.ObserveWorkload(ctx, desired.workload)
	if err != nil {
		return empty, false, fmt.Errorf("capture snapshot observation: %w", err)
	}
	if !validWorkloadObservation(observation, desired.workload) {
		return empty, false, ErrConflictingState
	}

	after, err := facade.readSnapshotJournal(ctx, project, request.Service)
	if err != nil {
		return empty, false, err
	}
	if !sameSnapshotJournal(before, after) {
		return empty, false, nil
	}

	preparation := prepareApplyEvidence(
		desired,
		observation,
		before.transaction,
		before.hasTransaction,
		before.applied,
		before.hasApplied,
	)
	preparation.Actions = slices.Clone(before.actions)
	preparation, err = classifyPreparedApply(preparation)
	if err != nil {
		return empty, false, err
	}

	return operationSnapshot(facade, preparation), true, nil
}

func (facade *ApplyFacade) readSnapshotJournal(
	ctx context.Context,
	project string,
	service string,
) (snapshotJournal, error) {
	var empty snapshotJournal

	reader, err := facade.openReader(ctx)
	if err != nil {
		return empty, fmt.Errorf("open snapshot state: %w", err)
	}
	if reader == nil {
		return empty, ErrInvalidRequest
	}

	journal, readErr := readSnapshotJournal(ctx, reader, project, service)
	closeErr := reader.Close()
	if readErr != nil || closeErr != nil {
		return empty, errors.Join(readErr, closeErr)
	}

	return journal, nil
}

func readSnapshotJournal(
	ctx context.Context,
	reader TransactionReader,
	project string,
	service string,
) (snapshotJournal, error) {
	var journal snapshotJournal

	transaction, found, err := reader.UnresolvedTransaction(ctx, project, service)
	if err != nil {
		return journal, fmt.Errorf("read snapshot transaction: %w", err)
	}
	journal.transaction = transaction
	journal.hasTransaction = found

	applied, hasApplied, err := reader.AppliedService(ctx, project, service)
	if err != nil {
		return snapshotJournal{}, fmt.Errorf("read snapshot applied service: %w", err)
	}
	journal.applied = applied
	journal.hasApplied = hasApplied

	if found {
		journal.actions, err = reader.Actions(ctx, transaction.ID)
		if err != nil {
			return snapshotJournal{}, fmt.Errorf("read snapshot actions: %w", err)
		}
	}

	return journal, nil
}

func sameSnapshotJournal(left, right snapshotJournal) bool {
	return left.transaction == right.transaction && left.hasTransaction == right.hasTransaction &&
		left.applied == right.applied && left.hasApplied == right.hasApplied &&
		sameSnapshotActions(left.actions, right.actions)
}

func sameSnapshotActions(left, right []store.Action) bool {
	if len(left) != len(right) {
		return false
	}

	for index := range left {
		if snapshotAction(left[index]) != snapshotAction(right[index]) {
			return false
		}
	}

	return true
}

func operationSnapshot(facade *ApplyFacade, preparation Preparation) OperationSnapshot {
	snapshot := OperationSnapshot{
		CapturedAt:     facade.now().UTC(),
		Plan:           preparation.Plan,
		Runtime:        preparation.Execution,
		HasTransaction: preparation.HasTransaction,
		HasApplied:     preparation.HasApplied,
		Actions:        snapshotActions(preparation.Actions),
		DroppedEvents:  droppedEvents(facade.events),
	}
	if preparation.HasTransaction {
		snapshot.Transaction = snapshotTransaction(preparation.Transaction)
	}
	if preparation.HasApplied {
		snapshot.Applied = snapshotAppliedService(preparation.Applied)
	}

	return snapshot
}

func snapshotTransaction(transaction store.Transaction) SnapshotTransaction {
	base := ""
	if transaction.HasBaseTransaction {
		base = transaction.BaseTransactionID.String()
	}

	return SnapshotTransaction{
		ID:              transaction.ID.String(),
		Kind:            string(transaction.Kind),
		State:           string(transaction.State),
		Runtime:         transaction.Runtime,
		Source:          transaction.SourceDigest.String(),
		Desired:         transaction.EffectiveDigest.String(),
		Execution:       transaction.ExecutionDigest.String(),
		BaseTransaction: base,
	}
}

func snapshotAppliedService(applied store.AppliedService) SnapshotAppliedService {
	return SnapshotAppliedService{
		Transaction:   applied.TransactionID.String(),
		Runtime:       applied.Runtime,
		Source:        applied.SourceDigest.String(),
		Desired:       applied.EffectiveDigest.String(),
		Execution:     applied.ExecutionDigest.String(),
		Configuration: applied.ConfigurationDigest.String(),
		Storage:       applied.StorageDigest.String(),
		Reference:     applied.ReferenceDigest.String(),
		Manifest:      applied.PlatformManifestDigest.String(),
		ImageConfig:   applied.ImageConfigDigest.String(),
	}
}

func snapshotActions(actions []store.Action) []SnapshotAction {
	projection := make([]SnapshotAction, len(actions))
	for index, action := range actions {
		projection[index] = snapshotAction(action)
	}

	return projection
}

func snapshotAction(action store.Action) SnapshotAction {
	postcondition := ""
	if action.PostconditionDigest != nil {
		postcondition = action.PostconditionDigest.String()
	}

	return SnapshotAction{
		Sequence:      action.Sequence,
		Kind:          action.Kind,
		State:         string(action.State),
		Intent:        action.IntentDigest.String(),
		Postcondition: postcondition,
	}
}

func droppedEvents(sink EventSink) (dropped uint64) {
	counter, valid := sink.(EventDropCounter)
	if !valid {
		return 0
	}

	defer func() {
		if recover() != nil {
			dropped = 0
		}
	}()

	return counter.DroppedEvents()
}
