//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func createUnknownJournal(
	t *testing.T,
	state *Store,
	lock *ServiceLock,
	intent TransactionIntent,
) (Transaction, ActionIntent) {
	t.Helper()

	record, err := lock.BeginTransaction(context.Background(), intent)
	requireNoError(t, err)
	assertTransaction(t, record, TransactionActive, intent)

	if len(record.ID.String()) != transactionIDBytes*2 || strings.ToLower(record.ID.String()) != record.ID.String() {
		t.Fatalf("transaction ID = %q", record.ID.String())
	}

	unresolved, found, err := state.UnresolvedTransaction(context.Background(), "project", "api")
	requireNoError(t, err)

	if !found || unresolved != record {
		t.Fatalf("UnresolvedTransaction() = %#v, %t", unresolved, found)
	}

	actions, err := state.Actions(context.Background(), record.ID)
	requireNoError(t, err)

	if len(actions) != 0 {
		t.Fatalf("initial actions = %#v", actions)
	}

	actionIntent := testActionIntent(1, "workload.create")
	action, err := lock.RecordActionIntent(context.Background(), record.ID, actionIntent)
	requireNoError(t, err)
	assertAction(t, action, actionIntent, ActionStateIntent, nil)

	reused, err := lock.RecordActionIntent(context.Background(), record.ID, actionIntent)
	requireNoError(t, err)
	assertAction(t, reused, actionIntent, ActionStateIntent, nil)

	action, err = lock.MarkActionEffectOutcomeUnknown(context.Background(), record.ID, actionIntent.Sequence)
	requireNoError(t, err)
	assertAction(t, action, actionIntent, ActionStateEffectOutcomeUnknown, nil)

	reused, err = lock.MarkActionEffectOutcomeUnknown(context.Background(), record.ID, actionIntent.Sequence)
	requireNoError(t, err)
	assertAction(t, reused, actionIntent, ActionStateEffectOutcomeUnknown, nil)

	return record, actionIntent
}

func assertUnknownJournal(
	t *testing.T,
	state *Store,
	lock *ServiceLock,
	identifier TransactionID,
	actionIntent ActionIntent,
) {
	t.Helper()

	unresolved, found, err := state.UnresolvedTransaction(context.Background(), "project", "api")
	requireNoError(t, err)

	if !found || unresolved.ID != identifier {
		t.Fatalf("reopened unresolved transaction = %#v, %t", unresolved, found)
	}

	actions, err := state.Actions(context.Background(), identifier)
	requireNoError(t, err)

	if len(actions) != 1 {
		t.Fatalf("reopened actions = %#v", actions)
	}

	assertAction(t, actions[0], actionIntent, ActionStateEffectOutcomeUnknown, nil)

	_, err = lock.RecordActionIntent(context.Background(), identifier, testActionIntent(2, "workload.remove"))
	assertErrorIs(t, err, ErrInvalidState)
}

func resolveJournal(
	t *testing.T,
	state *Store,
	lock *ServiceLock,
	record Transaction,
	intent TransactionIntent,
	actionIntent ActionIntent,
) {
	t.Helper()

	postcondition := domain.Hash([]byte("typed workload probe"))
	action, err := lock.CompleteAction(context.Background(), record.ID, actionIntent.Sequence, postcondition)
	requireNoError(t, err)
	assertAction(t, action, actionIntent, ActionStateCompleted, &postcondition)

	reused, err := lock.CompleteAction(context.Background(), record.ID, actionIntent.Sequence, postcondition)
	requireNoError(t, err)
	assertAction(t, reused, actionIntent, ActionStateCompleted, &postcondition)

	record, err = lock.SetTransactionState(context.Background(), record.ID, TransactionSucceeded)
	requireNoError(t, err)
	assertTransaction(t, record, TransactionSucceeded, intent)

	record, err = lock.SetTransactionState(context.Background(), record.ID, TransactionSucceeded)
	requireNoError(t, err)
	assertTransaction(t, record, TransactionSucceeded, intent)

	_, found, err := state.UnresolvedTransaction(context.Background(), "project", "api")
	requireNoError(t, err)

	if found {
		t.Fatal("terminal transaction remained unresolved")
	}
}

func createPendingJournal(t *testing.T, lock *ServiceLock) (Transaction, ActionIntent) {
	t.Helper()

	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimePodman))
	requireNoError(t, err)

	_, err = lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	assertErrorIs(t, err, ErrInvalidState)

	actionIntent := testActionIntent(1, "image.pull")
	action, err := lock.RecordActionIntent(context.Background(), transaction.ID, actionIntent)
	requireNoError(t, err)
	assertAction(t, action, actionIntent, ActionStateIntent, nil)

	return transaction, actionIntent
}

func assertPendingJournalGuards(
	t *testing.T,
	lock *ServiceLock,
	identifier TransactionID,
	actionIntent ActionIntent,
) {
	t.Helper()

	_, err := lock.RecordActionIntent(
		context.Background(),
		identifier,
		testActionIntent(actionIntent.Sequence, "image.remove"),
	)
	assertErrorIs(t, err, ErrInvalidState)

	_, err = lock.CompleteAction(
		context.Background(),
		identifier,
		actionIntent.Sequence,
		domain.Hash([]byte("premature")),
	)
	assertErrorIs(t, err, ErrInvalidState)

	_, err = lock.SetTransactionState(context.Background(), identifier, TransactionFailed)
	assertErrorIs(t, err, ErrInvalidState)
}

func resolveDegradedJournal(
	t *testing.T,
	lock *ServiceLock,
	identifier TransactionID,
	actionIntent ActionIntent,
) {
	t.Helper()

	_, err := lock.MarkActionEffectOutcomeUnknown(context.Background(), identifier, actionIntent.Sequence)
	requireNoError(t, err)

	postcondition := domain.Hash([]byte("pull result"))
	_, err = lock.CompleteAction(context.Background(), identifier, actionIntent.Sequence, postcondition)
	requireNoError(t, err)

	_, err = lock.CompleteAction(
		context.Background(),
		identifier,
		actionIntent.Sequence,
		domain.Hash([]byte("conflicting result")),
	)
	assertErrorIs(t, err, ErrInvalidState)

	degraded, err := lock.SetTransactionState(context.Background(), identifier, TransactionDegraded)
	requireNoError(t, err)

	if degraded.State != TransactionDegraded {
		t.Fatalf("degraded transaction = %#v", degraded)
	}

	restore := testActionIntent(2, "workload.restore")
	_, err = lock.RecordActionIntent(context.Background(), identifier, restore)
	requireNoError(t, err)
	_, err = lock.MarkActionEffectOutcomeUnknown(context.Background(), identifier, restore.Sequence)
	requireNoError(t, err)
	_, err = lock.CompleteAction(
		context.Background(),
		identifier,
		restore.Sequence,
		domain.Hash([]byte("restored original workload")),
	)
	requireNoError(t, err)

	failed, err := lock.SetTransactionState(context.Background(), identifier, TransactionFailed)
	requireNoError(t, err)

	if failed.State != TransactionFailed {
		t.Fatalf("failed transaction = %#v", failed)
	}
}

func openJournalTestStore(t *testing.T, path string) (*Store, *ServiceLock) {
	t.Helper()

	state := openJournalStore(t, path)
	lock := requireTryServiceLock(t, state, "project", "api")

	return state, lock
}

func openJournalStore(t *testing.T, path string) *Store {
	t.Helper()

	state, err := Open(context.Background(), path)
	if err != nil || state == nil {
		t.Fatalf("Open() = %#v, %v", state, err)
	}

	return state
}

func testTransactionIntent(runtime domain.RuntimeKind) TransactionIntent {
	return TransactionIntent{
		Runtime:         runtime,
		SourceDigest:    domain.Hash([]byte("source Compose")),
		EffectiveDigest: domain.Hash([]byte("normalized Compose")),
		ExecutionDigest: domain.Hash([]byte("runtime execution binding")),
	}
}

func testActionIntent(sequence int64, kind string) ActionIntent {
	return ActionIntent{
		Sequence:     sequence,
		Kind:         kind,
		IntentDigest: domain.Hash([]byte(kind)),
	}
}

func assertTransaction(
	t *testing.T,
	transaction Transaction,
	state TransactionState,
	intent TransactionIntent,
) {
	t.Helper()

	if transaction.ID == (TransactionID{}) || transaction.State != state ||
		transaction.Runtime != intent.Runtime || transaction.SourceDigest != intent.SourceDigest ||
		transaction.EffectiveDigest != intent.EffectiveDigest || transaction.ExecutionDigest != intent.ExecutionDigest {
		t.Fatalf("transaction = %#v", transaction)
	}
}

func assertAction(
	t *testing.T,
	action Action,
	intent ActionIntent,
	state ActionState,
	postcondition *domain.Digest,
) {
	t.Helper()

	if action.TransactionID == (TransactionID{}) || action.Sequence != intent.Sequence ||
		action.Kind != intent.Kind || action.State != state || action.IntentDigest != intent.IntentDigest {
		t.Fatalf("action = %#v", action)
	}

	if postcondition == nil {
		if action.PostconditionDigest != nil {
			t.Fatalf("action postcondition = %v", action.PostconditionDigest)
		}

		return
	}

	if action.PostconditionDigest == nil || *action.PostconditionDigest != *postcondition {
		t.Fatalf("action postcondition = %v, want %v", action.PostconditionDigest, postcondition)
	}
}

func assertErrorIs(t *testing.T, err error, target error) {
	t.Helper()

	if !errors.Is(err, target) {
		t.Fatalf("error = %v, want %v", err, target)
	}
}
