//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const journalHelperEnvironment = "MANIUD_JOURNAL_HELPER"

func TestJournalPersistsUnknownEffectAndResolvesByTypedProbe(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	state, lock := openJournalTestStore(t, path)
	intent := testTransactionIntent(domain.RuntimeDocker)
	record, actionIntent := createUnknownJournal(t, state, lock, intent)

	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())

	state, lock = openJournalTestStore(t, path)
	assertUnknownJournal(t, state, lock, record.ID, actionIntent)
	resolveJournal(t, state, lock, record, intent, actionIntent)

	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())
}

func TestJournalRejectsInvalidTransitionsAndConcurrentMutation(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	transaction, actionIntent := createPendingJournal(t, lock)
	assertPendingJournalGuards(t, lock, transaction.ID, actionIntent)
	resolveDegradedJournal(t, lock, transaction.ID, actionIntent)
}

func TestJournalRejectsInvalidInputsAndLostOwnership(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)

	invalidTransactionInputs := []TransactionIntent{
		testTransactionIntent(domain.RuntimeContainerd),
		testTransactionIntent(domain.RuntimeKind(testUnknownValue)),
	}
	for _, intent := range invalidTransactionInputs {
		_, err = lock.BeginTransaction(context.Background(), intent)
		assertErrorIs(t, err, ErrInvalidState)
	}

	invalidActionInputs := []ActionIntent{
		testActionIntent(0, "image.pull"),
		testActionIntent(-1, "image.pull"),
		testActionIntent(1, ""),
		testActionIntent(1, "UPPER"),
		testActionIntent(1, ".prefix"),
		testActionIntent(1, "contains/slash"),
		testActionIntent(1, strings.Repeat("a", 65)),
	}
	for _, intent := range invalidActionInputs {
		_, err = lock.RecordActionIntent(context.Background(), transaction.ID, intent)
		assertErrorIs(t, err, ErrInvalidState)
	}

	_, err = lock.MarkActionEffectOutcomeUnknown(context.Background(), transaction.ID, 0)
	assertErrorIs(t, err, ErrInvalidState)
	_, err = lock.CompleteAction(context.Background(), transaction.ID, -1, domain.Digest{})
	assertErrorIs(t, err, ErrInvalidState)
	_, err = lock.SetTransactionState(context.Background(), transaction.ID, TransactionActive)
	assertErrorIs(t, err, ErrInvalidState)

	unknown := TransactionID{1}
	_, err = state.Transaction(context.Background(), unknown)
	assertErrorIs(t, err, ErrInvalidState)
	_, err = state.Actions(context.Background(), unknown)
	assertErrorIs(t, err, ErrInvalidState)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = lock.RecordActionIntent(cancelled, transaction.ID, testActionIntent(1, "image.pull"))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RecordActionIntent(cancelled) = %v", err)
	}

	requireNoError(t, lock.Close())
	_, err = lock.RecordActionIntent(context.Background(), transaction.ID, testActionIntent(1, "image.pull"))
	assertErrorIs(t, err, ErrInvalidState)
	requireNoError(t, state.Close())

	_, _, err = state.UnresolvedTransaction(context.Background(), "project", "api")
	assertErrorIs(t, err, ErrInvalidState)
	_, err = state.Actions(context.Background(), transaction.ID)
	assertErrorIs(t, err, ErrInvalidState)
}

func TestJournalRecoversUnknownEffectAfterProcessDeath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	command, stdin := startJournalHelper(t, path)
	state := openJournalStore(t, path)

	contender, err := state.TryLockService("project", "api")
	if contender != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("TryLockService(child owner) = %#v, %v", contender, err)
	}

	requireNoError(t, command.Process.Kill())

	_ = stdin.Close()

	if command.Wait() == nil {
		t.Fatal("killed journal helper exited successfully")
	}

	lock := requireTryServiceLock(t, state, "project", "api")
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	transaction, found, err := state.UnresolvedTransaction(context.Background(), "project", "api")
	requireNoError(t, err)

	if !found || transaction.State != TransactionActive {
		t.Fatalf("post-crash transaction = %#v, %t", transaction, found)
	}

	actions, err := state.Actions(context.Background(), transaction.ID)
	requireNoError(t, err)

	if len(actions) != 1 || actions[0].State != ActionStateEffectOutcomeUnknown {
		t.Fatalf("post-crash actions = %#v", actions)
	}

	_, err = lock.RecordActionIntent(context.Background(), transaction.ID, testActionIntent(2, "workload.remove"))
	assertErrorIs(t, err, ErrInvalidState)
}

func TestJournalHelperProcess(t *testing.T) {
	t.Parallel()

	if os.Getenv(journalHelperEnvironment) != "1" {
		return
	}

	args := helperArguments(os.Args)
	if len(args) != 1 {
		panic("journal helper requires one state path")
	}

	state, err := Open(context.Background(), args[0])
	if err != nil {
		panic(err)
	}

	lock, err := state.TryLockService("project", "api")
	if err != nil {
		panic(err)
	}

	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	if err != nil {
		panic(err)
	}

	action, err := lock.RecordActionIntent(context.Background(), transaction.ID, testActionIntent(1, "workload.create"))
	if err != nil {
		panic(err)
	}

	_, err = lock.MarkActionEffectOutcomeUnknown(context.Background(), transaction.ID, action.Sequence)
	if err != nil {
		panic(err)
	}

	_, err = io.WriteString(os.Stdout, "ready\n")
	if err != nil {
		panic(err)
	}

	_, _ = io.Copy(io.Discard, os.Stdin)

	os.Exit(0)
}

func startJournalHelper(t *testing.T, path string) (*exec.Cmd, io.WriteCloser) {
	t.Helper()

	//nolint:gosec // The test re-executes its current test binary with a private temporary path.
	command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestJournalHelperProcess$", "--", path)

	command.Env = append(os.Environ(), journalHelperEnvironment+"=1")

	stdin, err := command.StdinPipe()
	requireNoError(t, err)

	if stdin == nil {
		t.Fatal("journal helper stdin pipe is nil")
	}

	stdout, err := command.StdoutPipe()
	requireNoError(t, err)

	if stdout == nil {
		t.Fatal("journal helper stdout pipe is nil")
	}

	requireNoError(t, command.Start())

	ready := make([]byte, len("ready\n"))

	_, err = io.ReadFull(stdout, ready)
	if err != nil || string(ready) != "ready\n" {
		_ = command.Process.Kill()
		_ = command.Wait()

		t.Fatalf("journal helper did not become ready: %q, %v", ready, err)
	}

	return command, stdin
}
