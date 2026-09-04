//go:build linux || darwin

package store

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

const writerLeaseHelperEnvironment = "MANIUD_WRITER_LEASE_HELPER"

func TestWriterLeaseAcquiresReleasesAndAdvancesEpoch(t *testing.T) {
	t.Parallel()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))

	first := requireTryServiceLock(t, state, "project", "api")
	if first.lease.epoch != 1 {
		t.Fatalf("first writer epoch = %d", first.lease.epoch)
	}

	err := first.Fence(context.Background())
	if err != nil {
		t.Fatalf("first Fence() = %v", err)
	}

	assertWriterLeaseRow(t, state.database, first.lease, false)
	requireNoError(t, first.Close())
	assertWriterLeaseRow(t, state.database, first.lease, true)

	if !errors.Is(first.Fence(context.Background()), ErrOwnershipLost) {
		t.Fatal("closed writer lease passed its fence")
	}

	second := requireTryServiceLock(t, state, "project", "api")
	if second.lease.epoch != 2 || second.lease.serviceID != first.lease.serviceID {
		t.Fatalf("second writer lease = %#v", second.lease)
	}

	t.Cleanup(func() {
		requireNoError(t, second.Close())
	})

	other := requireTryServiceLock(t, state, "project", "worker")
	if other.lease.epoch != 1 || other.lease.serviceID == second.lease.serviceID {
		t.Fatalf("other writer lease = %#v", other.lease)
	}

	requireNoError(t, other.Close())
}

func TestFencedWriteCommitsAndRollsBackTypedMutations(t *testing.T) {
	t.Parallel()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	_, err := state.database.ExecContext(
		context.Background(),
		"CREATE TABLE fenced_values (value TEXT PRIMARY KEY, epoch INTEGER NOT NULL)",
	)
	requireNoError(t, err)

	lock := requireTryServiceLock(t, state, "project", "api")
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
	})

	err = lock.withFencedWrite(context.Background(), func(
		ctx context.Context,
		transaction *sql.Tx,
		lease writerLease,
	) error {
		return insertFencedValue(ctx, transaction, lease, "committed")
	})
	requireNoError(t, err)

	err = lock.withFencedWrite(context.Background(), func(
		ctx context.Context,
		transaction *sql.Tx,
		lease writerLease,
	) error {
		writeErr := insertFencedValue(ctx, transaction, lease, "rolled-back")
		if writeErr != nil {
			return writeErr
		}

		return ErrUnavailable
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("withFencedWrite(rollback) = %v", err)
	}

	var (
		count int
		epoch int64
	)

	err = state.database.QueryRowContext(
		context.Background(),
		"SELECT count(*), min(epoch) FROM fenced_values",
	).Scan(&count, &epoch)
	if err != nil || count != 1 || epoch != lock.lease.epoch {
		t.Fatalf("fenced values = count %d, epoch %d, %v", count, epoch, err)
	}
}

func TestStaleDatabaseConnectionCannotPassWriterFence(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	staleStore := openServiceLockTestStore(t, path)
	currentStore := openServiceLockTestStore(t, path)

	staleLock := requireTryServiceLock(t, staleStore, "project", "api")
	staleLease := staleLock.lease
	requireNoError(t, staleLock.Close())

	currentLock := requireTryServiceLock(t, currentStore, "project", "api")
	t.Cleanup(func() {
		requireNoError(t, currentLock.Close())
	})

	transaction, err := staleStore.database.BeginTx(context.Background(), nil)
	requireNoError(t, err)

	err = proveWriterLease(context.Background(), transaction, staleLease)
	if !errors.Is(err, ErrOwnershipLost) {
		t.Fatalf("proveWriterLease(stale) = %v", err)
	}

	requireNoError(t, transaction.Rollback())
}

func TestWriterLeaseRecoversAfterProcessDeath(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	state := openServiceLockTestStore(t, path)

	command, stdin, childEpoch := startWriterLeaseHelper(t, path)

	contender, err := state.TryLockService("project", "api")
	if contender != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("TryLockService(child owner) = %#v, %v", contender, err)
	}

	requireNoError(t, command.Process.Kill())

	waitErr := command.Wait()
	_ = stdin.Close()

	if waitErr == nil {
		t.Fatal("killed writer helper exited successfully")
	}

	owner, err := state.TryLockService("project", "api")

	owner = requireServiceLock(t, owner, err)
	if owner.lease.epoch != childEpoch+1 {
		t.Fatalf("post-crash writer epoch = %d, want %d", owner.lease.epoch, childEpoch+1)
	}

	requireNoError(t, owner.Close())
}

func startWriterLeaseHelper(t *testing.T, path string) (*exec.Cmd, io.WriteCloser, int64) {
	t.Helper()

	//nolint:gosec // The test re-executes its current test binary with a private temporary path.
	command := exec.CommandContext(
		t.Context(),
		os.Args[0],
		"-test.run=^TestWriterLeaseHelperProcess$",
		"--",
		path,
	)

	command.Env = append(os.Environ(), writerLeaseHelperEnvironment+"=1")

	stdin, err := command.StdinPipe()
	requireNoError(t, err)

	if stdin == nil {
		t.Fatal("writer helper stdin pipe is nil")
	}

	stdout, err := command.StdoutPipe()
	requireNoError(t, err)

	if stdout == nil {
		t.Fatal("writer helper stdout pipe is nil")
	}

	var stderr bytes.Buffer

	command.Stderr = &stderr
	requireNoError(t, command.Start())

	scanner := bufio.NewScanner(io.LimitReader(stdout, 128))
	if !scanner.Scan() {
		_ = command.Process.Kill()
		_ = command.Wait()

		t.Fatalf("writer helper did not acquire its lease: %v, %q", scanner.Err(), stderr.String())
	}

	childEpoch, err := strconv.ParseInt(scanner.Text(), 10, 64)
	if err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()

		t.Fatalf("writer helper epoch is invalid: %v", err)
	}

	return command, stdin, childEpoch
}

func TestWriterLeaseHelperProcess(t *testing.T) {
	t.Parallel()

	if os.Getenv(writerLeaseHelperEnvironment) != "1" {
		return
	}

	args := helperArguments(os.Args)
	if len(args) != 1 {
		panic("writer lease helper requires one state path")
	}

	state, err := Open(context.Background(), args[0])
	if err != nil {
		panic(err)
	}

	lock, err := state.TryLockService("project", "api")
	if err != nil {
		panic(err)
	}

	_, err = fmt.Fprintf(os.Stdout, "%d\n", lock.lease.epoch)
	if err != nil {
		panic(err)
	}

	_, _ = io.Copy(io.Discard, os.Stdin)

	os.Exit(0)
}

func helperArguments(arguments []string) []string {
	for index, argument := range arguments {
		if argument == "--" {
			return arguments[index+1:]
		}
	}

	return nil
}

func assertWriterLeaseRow(t *testing.T, database *sql.DB, lease writerLease, ownerIsNull bool) {
	t.Helper()

	var (
		epoch int64
		owner []byte
	)

	err := database.QueryRowContext(
		context.Background(),
		"SELECT epoch, owner FROM writer_leases WHERE service_id = ?",
		lease.serviceID[:],
	).Scan(&epoch, &owner)
	if err != nil || epoch != lease.epoch || (owner == nil) != ownerIsNull {
		t.Fatalf("writer lease row = epoch %d, owner %x, %v", epoch, owner, err)
	}
}

func insertFencedValue(
	ctx context.Context,
	transaction *sql.Tx,
	lease writerLease,
	value string,
) error {
	result, err := transaction.ExecContext(
		ctx,
		"INSERT INTO fenced_values (value, epoch) "+
			"SELECT ?, ? WHERE EXISTS (SELECT 1 FROM writer_leases "+
			"WHERE service_id = ? AND epoch = ? AND owner = ?)",
		value,
		lease.epoch,
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	return requireWriterLeaseResult(result)
}
