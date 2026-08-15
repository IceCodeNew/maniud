package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type mismatchTest struct {
	name        string
	transaction store.Transaction
	actions     []store.Action
}

func TestPrepareRejectsMismatchedTransactionsAndActions(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)

	baseline, err := operation.service.Prepare(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("Prepare(baseline) error = %v", err)
	}

	exact := exactTransaction(baseline, store.TransactionActive)
	for _, test := range mismatchTests(exact) {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testMismatch(t, test)
		})
	}
}

func mismatchTests(exact store.Transaction) []mismatchTest {
	tests := transactionMismatchTests(exact)

	return append(tests, actionMismatchTests(exact)...)
}

func transactionMismatchTests(exact store.Transaction) []mismatchTest {
	return []mismatchTest{
		mismatchTransaction("runtime", exact, func(value *store.Transaction) {
			value.Runtime = domain.RuntimePodman
		}),
		mismatchTransaction("source", exact, func(value *store.Transaction) {
			value.SourceDigest = domain.Hash([]byte("other"))
		}),
		mismatchTransaction("desired", exact, func(value *store.Transaction) {
			value.EffectiveDigest = domain.Hash([]byte("other"))
		}),
		mismatchTransaction("execution", exact, func(value *store.Transaction) {
			value.ExecutionDigest = domain.Hash([]byte("other"))
		}),
		mismatchTransaction("terminal", exact, func(value *store.Transaction) {
			value.State = store.TransactionSucceeded
		}),
	}
}

func actionMismatchTests(exact store.Transaction) []mismatchTest {
	wrongTransaction := mutateAction(action(exact, 1, store.ActionStateIntent), func(value *store.Action) {
		value.TransactionID = store.TransactionID{2}
	})
	missingPostcondition := mutateAction(action(exact, 1, store.ActionStateCompleted), func(value *store.Action) {
		value.PostconditionDigest = nil
	})
	pendingPostcondition := mutateAction(action(exact, 1, store.ActionStateIntent), func(value *store.Action) {
		postcondition := domain.Hash([]byte("postcondition"))
		value.PostconditionDigest = &postcondition
	})
	degraded := mutateTransaction(exact, func(value *store.Transaction) {
		value.State = store.TransactionDegraded
	})

	return []mismatchTest{
		{name: "wrong transaction action", transaction: exact, actions: []store.Action{wrongTransaction}},
		{name: "sequence gap", transaction: exact, actions: []store.Action{
			action(exact, 2, store.ActionStateIntent),
		}},
		{name: "multiple pending", transaction: exact, actions: []store.Action{
			action(exact, 1, store.ActionStateIntent),
			action(exact, 2, store.ActionStateEffectOutcomeUnknown),
		}},
		{name: "completed after pending", transaction: exact, actions: []store.Action{
			action(exact, 1, store.ActionStateIntent), action(exact, 2, store.ActionStateCompleted),
		}},
		{name: "invalid action state", transaction: exact, actions: []store.Action{
			action(exact, 1, store.ActionState("invalid")),
		}},
		{name: "completed without postcondition", transaction: exact, actions: []store.Action{missingPostcondition}},
		{name: "pending with postcondition", transaction: exact, actions: []store.Action{pendingPostcondition}},
		{name: "degraded pending", transaction: degraded, actions: []store.Action{
			action(degraded, 1, store.ActionStateIntent),
		}},
	}
}

func mismatchTransaction(
	name string,
	exact store.Transaction,
	mutate func(*store.Transaction),
) mismatchTest {
	return mismatchTest{name: name, transaction: mutateTransaction(exact, mutate), actions: nil}
}

func testMismatch(t *testing.T, test mismatchTest) {
	t.Helper()

	operation := newTestOperation(t)
	operation.transactions.unresolved = func(
		context.Context,
		string,
		string,
	) (store.Transaction, bool, error) {
		return test.transaction, true, nil
	}
	operation.transactions.actions = func(
		context.Context,
		store.TransactionID,
	) ([]store.Action, error) {
		return test.actions, nil
	}

	_, err := operation.service.Prepare(context.Background(), operation.request)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("Prepare() error = %v, want ErrConflictingState", err)
	}
}

func TestPrepareContainsActionReadFailure(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)

	baseline, err := operation.service.Prepare(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("Prepare(baseline) error = %v", err)
	}

	transaction := exactTransaction(baseline, store.TransactionActive)
	operation.transactions.unresolved = func(context.Context, string, string) (store.Transaction, bool, error) {
		return transaction, true, nil
	}
	operation.transactions.actions = func(context.Context, store.TransactionID) ([]store.Action, error) {
		return nil, errTestBoundary
	}

	_, err = operation.service.Prepare(context.Background(), operation.request)
	if !errors.Is(err, errTestBoundary) {
		t.Fatalf("Prepare() error = %v, want boundary failure", err)
	}
}

func TestClassifiersRejectUnknownEnumValues(t *testing.T) {
	t.Parallel()

	unknownObservation := emptyObservation()

	unknownObservation.State = WorkloadObservationState(255)
	if validWorkloadObservation(unknownObservation) {
		t.Fatal("validWorkloadObservation(unknown) = true")
	}

	_, err := classifyNewApply(unknownObservation, emptyDesiredWorkload())
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyNewApply(unknown) error = %v", err)
	}

	_, err = classifyNewApply(emptyObservation(), emptyDesiredWorkload())
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyNewApply(zero) error = %v", err)
	}

	transaction := emptyTransaction()
	transaction.State = store.TransactionState("unknown")

	_, err = classifyRecovery(transaction, nil)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyRecovery(unknown) error = %v", err)
	}

	for _, state := range []store.ActionState{store.ActionStateCompleted, store.ActionState("unknown")} {
		pending := action(transaction, 1, state)

		_, err = classifyActiveRecovery(&pending)
		if !errors.Is(err, ErrConflictingState) {
			t.Fatalf("classifyActiveRecovery(%q) error = %v", state, err)
		}
	}
}

func TestObservedApplyRejectsConflictingEvidence(t *testing.T) {
	t.Parallel()

	workload := emptyDesiredWorkload()
	for _, observation := range []WorkloadObservation{
		presentObservation(true, true, domain.OwnershipConflicting),
		presentObservation(false, true, domain.OwnershipUnmanaged),
		presentObservation(true, true, domain.OwnershipStatus(255)),
	} {
		_, err := classifyObservedApply(observation, workload)
		if !errors.Is(err, ErrConflictingState) {
			t.Fatalf("classifyObservedApply(%#v) error = %v", observation, err)
		}
	}
}

func mutateTransaction(
	value store.Transaction,
	mutate func(*store.Transaction),
) store.Transaction {
	mutate(&value)

	return value
}

func mutateAction(value store.Action, mutate func(*store.Action)) store.Action {
	mutate(&value)

	return value
}
