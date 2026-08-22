//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
	"golang.org/x/sys/unix"
)

func TestOpenReaderTreatsMissingStateAsEmptyWithoutCreatingFiles(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	paths := []string{
		filepath.Join(directory, "state.db"),
		filepath.Join(directory, "missing", "state.db"),
	}

	for _, path := range paths {
		assertMissingStateReader(t, path)
	}

	entries, err := os.ReadDir(directory)
	requireNoError(t, err)

	if len(entries) != 0 {
		t.Fatalf("reader created entries = %#v", entries)
	}
}

func assertMissingStateReader(t *testing.T, path string) {
	t.Helper()

	reader := requireOpenReader(t, path)
	assertMissingReaderTransaction(t, reader)
	assertMissingReaderAppliedService(t, reader)
	assertMissingReaderBackup(t, reader)

	_, err := reader.Actions(context.Background(), TransactionID{})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Actions() error = %v", err)
	}

	requireNoError(t, reader.Close())

	_, err = os.Stat(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state path after read = %v", err)
	}
}

func assertMissingReaderTransaction(t *testing.T, reader *Reader) {
	t.Helper()

	transaction, found, err := reader.UnresolvedTransaction(context.Background(), "project", "api")
	if err != nil || found || transaction != (Transaction{}) {
		t.Fatalf("UnresolvedTransaction() = %#v, %t, %v", transaction, found, err)
	}
}

func assertMissingReaderAppliedService(t *testing.T, reader *Reader) {
	t.Helper()

	applied, found, err := reader.AppliedService(context.Background(), "project", "api")
	if err != nil || found || applied != (AppliedService{}) {
		t.Fatalf("AppliedService() = %#v, %t, %v", applied, found, err)
	}
}

func assertMissingReaderBackup(t *testing.T, reader *Reader) {
	t.Helper()

	backup, found, err := reader.BackupIndex(context.Background(), TransactionID{1})
	if err != nil || found || backup != (BackupIndex{}) {
		t.Fatalf("BackupIndex() = %#v, %t, %v", backup, found, err)
	}
}

func TestOpenReaderReadsAppliedServiceBaseline(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	state, lock := openJournalTestStore(t, path)
	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)

	want, err := lock.CommitAppliedService(context.Background(), transaction.ID, testAppliedServiceIntent())
	requireNoError(t, err)
	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())

	reader := requireOpenReader(t, path)
	got, found, err := reader.AppliedService(context.Background(), "project", "api")
	if err != nil || !found || got != want {
		t.Fatalf("AppliedService() = %#v, %t, %v, want %#v", got, found, err, want)
	}

	requireNoError(t, reader.Close())
}

func TestOpenReaderReadsBackupIndex(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	state, lock := openJournalTestStore(t, path)
	_, upgrade := beginUpgradeForBackupTest(t, lock)
	intent := testAppliedServiceIntent()
	intent.WorkloadID = replacementWorkloadID
	intent.Backup = testBackupIndexIntent(upgrade.ID)
	_, err := lock.CommitAppliedService(context.Background(), upgrade.ID, intent)
	requireNoError(t, err)
	want, found, err := state.BackupIndex(context.Background(), upgrade.ID)
	if err != nil || !found {
		t.Fatalf("Store.BackupIndex() = %#v, %t, %v", want, found, err)
	}
	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())

	reader := requireOpenReader(t, path)
	got, found, err := reader.BackupIndex(context.Background(), upgrade.ID)
	if err != nil || !found || got != want {
		t.Fatalf("Reader.BackupIndex() = %#v, %t, %v, want %#v", got, found, err, want)
	}
	requireNoError(t, reader.Close())
}

func TestOpenReaderReadsCheckpointedJournalWithoutCreatingSidecars(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	path := filepath.Join(directory, "state.db")
	state, lock := openJournalTestStore(t, path)
	record, intent := createUnknownJournal(t, state, lock, testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())

	before := directorySnapshot(t, directory)
	reader := requireOpenReader(t, path)

	got, found, err := reader.UnresolvedTransaction(context.Background(), "project", "api")
	if err != nil || !found || got != record {
		t.Fatalf("UnresolvedTransaction() = %#v, %t, %v", got, found, err)
	}

	actions, err := reader.Actions(context.Background(), record.ID)
	if err != nil || len(actions) != 1 {
		t.Fatalf("Actions() = %#v, %v", actions, err)
	}

	assertAction(t, actions[0], intent, ActionStateEffectOutcomeUnknown, nil)

	requireNoError(t, reader.Close())

	after := directorySnapshot(t, directory)
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("checkpointed state changed: before=%#v after=%#v", before, after)
	}
}

func TestOpenReaderKeepsOneSnapshotWithConcurrentWriter(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	path := filepath.Join(directory, "state.db")
	state, lock := openJournalTestStore(t, path)
	record, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)

	databaseBefore := readFile(t, path)
	walBefore := readFile(t, path+"-wal")

	reader := requireOpenReader(t, path)

	got, found, err := reader.UnresolvedTransaction(context.Background(), "project", "api")
	if err != nil || !found || got != record {
		t.Fatalf("UnresolvedTransaction() = %#v, %t, %v", got, found, err)
	}

	_, err = lock.RecordActionIntent(context.Background(), record.ID, testActionIntent(1, "workload.create"))
	requireNoError(t, err)

	actions, err := reader.Actions(context.Background(), record.ID)
	if err != nil || len(actions) != 0 {
		t.Fatalf("snapshot Actions() = %#v, %v", actions, err)
	}

	requireNoError(t, reader.Close())

	if bytes.Equal(databaseBefore, readFile(t, path)) && bytes.Equal(walBefore, readFile(t, path+"-wal")) {
		t.Fatal("concurrent journal mutation did not change durable state")
	}

	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())
}

func TestOpenReaderAndStoreExcludeConcurrentStartup(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	state := openJournalStore(t, path)
	requireNoError(t, state.Close())

	reader := requireOpenReader(t, path)

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	contender, err := Open(ctx, path)
	if contender != nil || !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Open(during read snapshot) = %#v, %v", contender, err)
	}

	requireNoError(t, reader.Close())

	state = openJournalStore(t, path)
	requireNoError(t, state.Close())
}

//nolint:funlen // The table keeps every invalid durable-state shape under one assertion.
func TestOpenReaderRejectsInvalidStateWithoutRepair(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
		want  error
	}{
		{
			name: "database without lock",
			setup: func(t *testing.T, path string) {
				t.Helper()
				requireNoError(t, os.WriteFile(path, nil, privateFileMode))
			},
			want: ErrInvalidState,
		},
		{
			name: "lock without database",
			setup: func(t *testing.T, path string) {
				t.Helper()
				requireNoError(t, os.WriteFile(path+".lock", nil, privateFileMode))
			},
			want: ErrInvalidState,
		},
		{
			name: "partial sidecars",
			setup: func(t *testing.T, path string) {
				t.Helper()
				state := openJournalStore(t, path)
				requireNoError(t, state.Close())
				requireNoError(t, os.WriteFile(path+"-wal", nil, privateFileMode))
			},
			want: ErrInvalidState,
		},
		{
			name: "incomplete v1 schema",
			setup: func(t *testing.T, path string) {
				t.Helper()
				state := openJournalStore(t, path)
				_, err := state.database.ExecContext(context.Background(), "DROP TABLE workload_backups")
				requireNoError(t, err)
				requireNoError(t, state.Close())
			},
			want: ErrInvalidState,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := privateTempDir(t)
			path := filepath.Join(directory, "state.db")
			test.setup(t, path)
			before := directorySnapshot(t, directory)

			reader, err := OpenReader(context.Background(), path)
			if reader != nil || !errors.Is(err, test.want) {
				t.Fatalf("OpenReader() = %#v, %v", reader, err)
			}

			after := directorySnapshot(t, directory)
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected state changed: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestOpenReaderRejectsInvalidPathAndCancellation(t *testing.T) {
	t.Parallel()

	reader, err := OpenReader(context.Background(), "state.db")
	if reader != nil || !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("OpenReader(relative) = %#v, %v", reader, err)
	}

	path := filepath.Join(privateTempDir(t), "state.db")
	state := openJournalStore(t, path)
	requireNoError(t, state.Close())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	reader, err = OpenReader(ctx, path)
	if reader != nil || !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("OpenReader(cancelled) = %#v, %v", reader, err)
	}
}

//nolint:funlen // The table keeps every unsafe filesystem shape under one assertion.
func TestOpenReaderRejectsUnsafeFilesystemEntries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		setup func(*testing.T, string, string)
	}{
		{
			name: "non-directory parent",
			setup: func(t *testing.T, directory, _ string) {
				t.Helper()
				requireNoError(t, os.WriteFile(filepath.Join(directory, "parent"), nil, 0o600))
			},
		},
		{
			name: "broad parent",
			setup: func(t *testing.T, directory, _ string) {
				t.Helper()
				requireNoError(t, os.Mkdir(filepath.Join(directory, "parent"), 0o700))
				requireNoError(t, unix.Chmod(filepath.Join(directory, "parent"), 0o755))
			},
		},
		{
			name: "symlink lock",
			setup: func(t *testing.T, directory, path string) {
				t.Helper()
				requireNoError(t, os.Mkdir(filepath.Join(directory, "parent"), 0o700))
				requireNoError(t, os.Symlink("target", path+".lock"))
			},
		},
		{
			name: "broad lock",
			setup: func(t *testing.T, directory, path string) {
				t.Helper()
				requireNoError(t, os.Mkdir(filepath.Join(directory, "parent"), 0o700))
				requireNoError(t, os.WriteFile(path+".lock", nil, 0o600))
				requireNoError(t, unix.Chmod(path+".lock", 0o644))
			},
		},
		{
			name: "broad database",
			setup: func(t *testing.T, directory, path string) {
				t.Helper()
				requireNoError(t, os.Mkdir(filepath.Join(directory, "parent"), 0o700))
				requireNoError(t, os.WriteFile(path+".lock", nil, 0o600))
				requireNoError(t, os.WriteFile(path, nil, 0o600))
				requireNoError(t, unix.Chmod(path, 0o644))
			},
		},
		{
			name: "fifo without lock",
			setup: func(t *testing.T, directory, path string) {
				t.Helper()
				requireNoError(t, os.Mkdir(filepath.Join(directory, "parent"), 0o700))
				requireNoError(t, unix.Mkfifo(path, 0o600))
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := privateTempDir(t)
			path := filepath.Join(directory, "parent", "state.db")
			test.setup(t, directory, path)

			reader, err := OpenReader(context.Background(), path)
			if reader != nil || !errors.Is(err, ErrInvalidState) {
				t.Fatalf("OpenReader() = %#v, %v", reader, err)
			}
		})
	}
}

func TestReaderContainsInvalidRequests(t *testing.T) {
	t.Parallel()

	missing := requireOpenReader(t, filepath.Join(privateTempDir(t), "state.db"))
	defer func() { requireNoError(t, missing.Close()) }()

	var err error

	_, _, err = missing.UnresolvedTransaction(context.Background(), "", "api")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("empty UnresolvedTransaction(invalid) error = %v", err)
	}

	_, _, err = missing.AppliedService(context.Background(), "", "api")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("empty AppliedService(invalid) error = %v", err)
	}

	_, _, err = missing.BackupIndex(context.Background(), TransactionID{})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("empty BackupIndex(invalid) error = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = missing.UnresolvedTransaction(cancelled, "project", "api")
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("empty UnresolvedTransaction(cancelled) error = %v", err)
	}

	_, _, err = missing.AppliedService(cancelled, "project", "api")
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("empty AppliedService(cancelled) error = %v", err)
	}

	_, _, err = missing.BackupIndex(cancelled, TransactionID{1})
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("empty BackupIndex(cancelled) error = %v", err)
	}
}

func TestReaderContainsClosedResources(t *testing.T) {
	t.Parallel()

	missing := requireOpenReader(t, filepath.Join(privateTempDir(t), "state.db"))

	requireNoError(t, missing.Close())

	if !errors.Is(missing.Close(), ErrUnavailable) || !errors.Is((*Reader)(nil).Close(), ErrUnavailable) {
		t.Fatal("closed or nil Reader closed successfully")
	}

	_, _, err := missing.UnresolvedTransaction(context.Background(), "project", "api")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("closed UnresolvedTransaction() error = %v", err)
	}

	_, _, err = missing.AppliedService(context.Background(), "project", "api")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("closed AppliedService() error = %v", err)
	}

	_, _, err = missing.BackupIndex(context.Background(), TransactionID{1})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("closed BackupIndex() error = %v", err)
	}

	_, err = missing.Actions(context.Background(), TransactionID{})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("closed Actions() error = %v", err)
	}
}

func TestReaderContainsSQLiteAndIdentityFailures(t *testing.T) {
	t.Parallel()

	newReader := func(t *testing.T) *Reader {
		t.Helper()

		path := filepath.Join(privateTempDir(t), "state.db")
		state := openJournalStore(t, path)
		requireNoError(t, state.Close())

		return requireOpenReader(t, path)
	}

	rolledBack := newReader(t)
	requireNoError(t, rolledBack.transaction.Rollback())
	assertInvalidReaderQueries(t, rolledBack, "rolled back")

	if !errors.Is(rolledBack.Close(), ErrUnavailable) {
		t.Fatal("Close(rolled back) did not report unavailable state")
	}

	replaced := newReader(t)
	replaced.anchor.directoryID = fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}
	assertInvalidReaderQueries(t, replaced, "replaced")

	if !errors.Is(replaced.Close(), ErrInvalidState) {
		t.Fatal("Close(replaced) did not report invalid state")
	}
}

func assertInvalidReaderQueries(t *testing.T, reader *Reader, label string) {
	t.Helper()

	_, _, err := reader.UnresolvedTransaction(context.Background(), "project", "api")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("UnresolvedTransaction(%s) error = %v", label, err)
	}

	_, _, err = reader.AppliedService(context.Background(), "project", "api")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("AppliedService(%s) error = %v", label, err)
	}

	_, _, err = reader.BackupIndex(context.Background(), TransactionID{1})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("BackupIndex(%s) error = %v", label, err)
	}

	_, err = reader.Actions(context.Background(), TransactionID{})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Actions(%s) error = %v", label, err)
	}
}

//nolint:cyclop,funlen // Each branch is one independent stable-error classification assertion.
func TestReaderInternalGuardsClassifyFailures(t *testing.T) {
	t.Parallel()

	if readerEntryError(nil) != nil || readerEntryError(os.ErrNotExist) != nil ||
		!errors.Is(readerEntryError(errJournalTest), errJournalTest) {
		t.Fatal("readerEntryError() misclassified an entry result")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err := classifyReaderOpen(cancelled, ErrInvalidState)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("classifyReaderOpen(cancelled) = %v", err)
	}

	err = classifyReaderOpen(context.Background(), ErrUnavailable)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("classifyReaderOpen(unavailable) = %v", err)
	}

	err = classifyReaderOpen(context.Background(), errJournalTest)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("classifyReaderOpen(invalid) = %v", err)
	}

	err = classifyReaderValidation(cancelled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("classifyReaderValidation(cancelled) = %v", err)
	}

	err = classifyReaderValidation(context.Background())
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("classifyReaderValidation(invalid) = %v", err)
	}

	if !errors.Is((*Reader)(nil).requireValid(), ErrInvalidState) {
		t.Fatal("nil reader capability passed validation")
	}

	if !errors.Is(validateReaderSnapshot(context.Background(), nil), ErrInvalidState) {
		t.Fatal("validateReaderSnapshot(nil) accepted a nil reader")
	}

	invalidReader := &Reader{
		database:    nil,
		transaction: nil,
		anchor:      nil,
		sidecars:    false,
		closed:      true,
	}
	if !errors.Is(validateReaderSnapshot(context.Background(), invalidReader), ErrInvalidState) {
		t.Fatal("validateReaderSnapshot(invalid) accepted an invalid reader")
	}
	if _, _, err := invalidReader.backupResult(BackupIndex{}, false); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("backupResult(invalid) error = %v", err)
	}

	var nilReader *Reader

	_, _, err = nilReader.UnresolvedTransaction(context.Background(), "project", "api")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("nil UnresolvedTransaction() error = %v", err)
	}

	_, _, err = nilReader.AppliedService(context.Background(), "project", "api")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("nil AppliedService() error = %v", err)
	}

	_, _, err = nilReader.BackupIndex(context.Background(), TransactionID{1})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("nil BackupIndex() error = %v", err)
	}

	_, err = nilReader.Actions(context.Background(), TransactionID{})
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("nil Actions() error = %v", err)
	}
}

//nolint:cyclop,funlen // Fault injection keeps the private opener cleanup paths together.
func TestReaderInternalFaultBoundaries(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	state := openJournalStore(t, path)
	requireNoError(t, state.Close())

	reader, err := finishOpenReader(context.Background(), nil)
	if reader != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("finishOpenReader(nil) = %#v, %v", reader, err)
	}

	anchor, missing, err := openReaderAnchor(context.Background(), path)
	if err != nil || missing || anchor == nil {
		t.Fatalf("openReaderAnchor() = %#v, %t, %v", anchor, missing, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	reader, err = finishOpenReader(cancelled, anchor)
	if reader != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("finishOpenReader(cancelled) = %#v, %v", reader, err)
	}

	_, err = readerDirectoryHasNoState(
		anchor,
		func(string) ([]os.DirEntry, error) { return nil, errJournalTest },
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("readerDirectoryHasNoState() = %v", err)
	}

	settingsReader := requireOpenReader(t, path)
	_, err = settingsReader.transaction.ExecContext(context.Background(), "PRAGMA query_only = OFF")
	requireNoError(t, err)

	err = validateReaderSnapshot(context.Background(), settingsReader)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("validateReaderSnapshot(settings) = %v", err)
	}

	requireNoError(t, settingsReader.Close())

	var empty Transaction

	invalid := &Reader{
		database:    nil,
		transaction: nil,
		anchor:      nil,
		sidecars:    false,
		closed:      true,
	}

	_, _, err = invalid.unresolvedResult(empty, false)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unresolvedResult(invalid) = %v", err)
	}

	_, _, err = invalid.appliedResult(AppliedService{}, false)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("appliedResult(invalid) = %v", err)
	}

	_, err = invalid.actionResult(nil)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("actionResult(invalid) = %v", err)
	}
}

type fileSnapshot struct {
	name string
	mode os.FileMode
	size int64
	data []byte
}

func directorySnapshot(t *testing.T, directory string) []fileSnapshot {
	t.Helper()

	entries, err := os.ReadDir(directory)
	requireNoError(t, err)

	snapshot := make([]fileSnapshot, 0, len(entries))
	for _, entry := range entries {
		metadata, statErr := entry.Info()
		if statErr != nil || metadata == nil {
			t.Fatal(statErr)
		}

		data := []byte(nil)
		if metadata.Mode().IsRegular() {
			data = readFile(t, filepath.Join(directory, entry.Name()))
		}

		snapshot = append(snapshot, fileSnapshot{
			name: entry.Name(),
			mode: metadata.Mode(),
			size: metadata.Size(),
			data: data,
		})
	}

	return snapshot
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	content, err := os.ReadFile(path) //nolint:gosec // Tests read private temporary paths.
	requireNoError(t, err)

	return content
}

func requireOpenReader(t *testing.T, path string) *Reader {
	t.Helper()

	reader, err := OpenReader(context.Background(), path)
	if err != nil || reader == nil {
		t.Fatalf("OpenReader(%q) = %#v, %v", path, reader, err)
	}

	return reader
}
