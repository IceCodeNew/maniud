//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"

	modernsqlite "modernc.org/sqlite"
)

// Reader owns one non-creating, current-schema snapshot of maniud's durable
// transaction state. Close releases the SQLite snapshot and startup lock.
type Reader struct {
	database    *sql.DB
	transaction *sql.Tx
	anchor      *stateAnchor
	sidecars    bool
	closed      bool
}

// OpenReader opens current durable state without creating files, recovering a
// backup, or changing application state. A missing state database is
// represented by an empty Reader.
func OpenReader(ctx context.Context, path string) (*Reader, error) {
	if !validStatePath(path) {
		return nil, ErrInvalidPath
	}

	if ctx.Err() != nil {
		return nil, classifyContext(ctx)
	}

	anchor, missing, err := openReaderAnchor(ctx, path)
	if err != nil {
		return nil, err
	}

	if missing {
		return &Reader{
			database:    nil,
			transaction: nil,
			anchor:      nil,
			sidecars:    false,
			closed:      false,
		}, nil
	}

	reader, err := finishOpenReader(ctx, anchor)
	if err != nil {
		if anchor != nil {
			_ = anchor.close()
		}

		return nil, err
	}

	return reader, nil
}

func openReaderAnchor(ctx context.Context, path string) (*stateAnchor, bool, error) {
	directoryPath := filepath.Dir(path)

	directory, err := openDirectory(directoryPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, true, nil
	}

	if err != nil {
		return nil, false, ErrInvalidState
	}

	anchor := newStateAnchor(directory, directoryPath, filepath.Base(path))
	if !anchor.captureDirectory() {
		_ = anchor.close()

		return nil, false, ErrInvalidState
	}

	err = anchor.openExistingLock(ctx)
	if errors.Is(err, os.ErrNotExist) {
		missing, inspectErr := readerDatabaseMissing(anchor)
		_ = anchor.close()

		return nil, missing && inspectErr == nil, inspectErr
	}

	if err != nil {
		_ = anchor.close()

		return nil, false, classifyReaderOpen(ctx, err)
	}

	_, err = anchor.openReaderDatabase()
	if err != nil {
		_ = anchor.close()

		return nil, false, err
	}

	return anchor, false, nil
}

func readerDatabaseMissing(anchor *stateAnchor) (bool, error) {
	_, databaseFound, databaseErr := anchor.openExistingRegular(anchor.databaseName)
	_, walFound, walErr := anchor.openExistingRegular(anchor.databaseName + "-wal")
	_, sharedFound, sharedErr := anchor.openExistingRegular(anchor.databaseName + "-shm")

	if readerEntryError(databaseErr) != nil || readerEntryError(walErr) != nil || readerEntryError(sharedErr) != nil {
		return false, ErrInvalidState
	}

	if databaseFound || walFound || sharedFound {
		return false, ErrInvalidState
	}

	return readerDirectoryHasNoState(anchor, os.ReadDir)
}

func readerDirectoryHasNoState(
	anchor *stateAnchor,
	readDir func(string) ([]os.DirEntry, error),
) (bool, error) {
	_, err := readDir(platformEntryPath(anchor, "."))
	if err != nil {
		return false, ErrUnavailable
	}

	return true, nil
}

func (anchor *stateAnchor) openReaderDatabase() (bool, error) {
	databaseID, databaseFound, databaseErr := anchor.openExistingRegular(anchor.databaseName)
	walID, walFound, walErr := anchor.openExistingRegular(anchor.databaseName + "-wal")
	sharedID, sharedFound, sharedErr := anchor.openExistingRegular(anchor.databaseName + "-shm")

	if readerEntryError(databaseErr) != nil || readerEntryError(walErr) != nil || readerEntryError(sharedErr) != nil ||
		!databaseFound || walFound != sharedFound {
		return false, ErrInvalidState
	}

	anchor.databaseID = databaseID
	anchor.walID = walID
	anchor.sharedID = sharedID

	return walFound, nil
}

func readerEntryError(err error) error {
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}

	return err
}

func finishOpenReader(ctx context.Context, anchor *stateAnchor) (*Reader, error) {
	if anchor == nil {
		return nil, ErrInvalidState
	}

	emptyIdentity := fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}
	sidecars := anchor.walID != emptyIdentity

	connector, _ := modernsqlite.NewConnector(sqliteJournalReadOnlyURI(anchor.databasePath(), !sidecars))
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	transaction, err := database.BeginTx(ctx, &sql.TxOptions{
		Isolation: sql.LevelDefault,
		ReadOnly:  true,
	})
	if err != nil {
		_ = database.Close()

		return nil, classifySQLiteProbe(ctx, err)
	}

	reader := &Reader{
		database:    database,
		transaction: transaction,
		anchor:      anchor,
		sidecars:    sidecars,
		closed:      false,
	}

	err = validateReaderSnapshot(ctx, reader)
	if err != nil {
		_ = transaction.Rollback()
		_ = database.Close()

		return nil, err
	}

	return reader, nil
}

func validateReaderSnapshot(ctx context.Context, reader *Reader) error {
	if reader == nil {
		return ErrInvalidState
	}

	err := reader.requireValid()
	if err != nil {
		return err
	}

	if !validReaderSettings(ctx, reader.transaction, reader.sidecars) {
		return classifyReaderValidation(ctx)
	}

	err = validateSchema(ctx, reader.transaction)
	if err != nil {
		return err
	}

	return reader.requireValid()
}

func validReaderSettings(ctx context.Context, query rowQueryer, sidecars bool) bool {
	var (
		foreignKeys int
		journalMode string
		queryOnly   int
		lockWait    int
	)

	err := query.QueryRowContext(
		ctx,
		"SELECT foreign_keys, journal_mode, query_only, timeout "+
			"FROM pragma_foreign_keys, pragma_journal_mode, pragma_query_only, pragma_busy_timeout",
	).Scan(&foreignKeys, &journalMode, &queryOnly, &lockWait)

	wantJournalMode := "delete"
	if sidecars {
		wantJournalMode = "wal"
	}

	return err == nil && foreignKeys == 1 && journalMode == wantJournalMode && queryOnly == 1 &&
		lockWait == int(busyTimeout.Milliseconds())
}

func (anchor *stateAnchor) readerValid(sidecars bool) bool {
	if anchor == nil || anchor.lock < 0 || anchor.locked || !anchor.validPersistentEntries() {
		return false
	}

	if !sidecars {
		_, walValid := entryIdentity(anchor.directory, anchor.databaseName+"-wal")
		_, sharedValid := entryIdentity(anchor.directory, anchor.databaseName+"-shm")

		return !walValid && !sharedValid
	}

	return anchor.validEntry(anchor.databaseName+"-wal", anchor.walID) &&
		anchor.validEntry(anchor.databaseName+"-shm", anchor.sharedID)
}

// UnresolvedTransaction returns the sole active or degraded transaction from
// the Reader's stable snapshot.
func (reader *Reader) UnresolvedTransaction(
	ctx context.Context,
	projectName string,
	serviceName string,
) (Transaction, bool, error) {
	if reader == nil {
		return Transaction{}, false, ErrInvalidState
	}

	if !reader.closed && reader.database == nil && reader.transaction == nil && reader.anchor == nil {
		_, valid := serviceIdentity(projectName, serviceName)
		if !valid {
			return Transaction{}, false, ErrInvalidState
		}

		if ctx.Err() != nil {
			return Transaction{}, false, classifyContext(ctx)
		}

		var empty Transaction

		return empty, false, nil
	}

	err := reader.requireValid()
	if err != nil {
		return Transaction{}, false, ErrInvalidState
	}

	record, found, err := unresolvedTransaction(ctx, reader.transaction, projectName, serviceName)
	if err != nil {
		return Transaction{}, false, err
	}

	return reader.unresolvedResult(record, found)
}

// AppliedService returns the latest committed workload generation from the
// Reader's stable snapshot.
func (reader *Reader) AppliedService(
	ctx context.Context,
	projectName string,
	serviceName string,
) (AppliedService, bool, error) {
	if reader == nil {
		return AppliedService{}, false, ErrInvalidState
	}

	serviceID, valid := serviceIdentity(projectName, serviceName)
	if !valid {
		return AppliedService{}, false, ErrInvalidState
	}

	if !reader.closed && reader.database == nil && reader.transaction == nil && reader.anchor == nil {
		if ctx.Err() != nil {
			return AppliedService{}, false, classifyContext(ctx)
		}

		return AppliedService{}, false, nil
	}

	if reader.requireValid() != nil {
		return AppliedService{}, false, ErrInvalidState
	}

	record, found, err := appliedService(ctx, reader.transaction, serviceID)
	if err != nil {
		return AppliedService{}, false, err
	}

	return reader.appliedResult(record, found)
}

// BackupIndex returns one complete-manifest locator from the Reader's stable
// snapshot.
func (reader *Reader) BackupIndex(
	ctx context.Context,
	identifier TransactionID,
) (BackupIndex, bool, error) {
	if reader == nil || identifier == (TransactionID{}) {
		return BackupIndex{}, false, ErrInvalidState
	}

	if !reader.closed && reader.database == nil && reader.transaction == nil && reader.anchor == nil {
		if ctx.Err() != nil {
			return BackupIndex{}, false, classifyContext(ctx)
		}

		return BackupIndex{}, false, nil
	}

	if reader.requireValid() != nil {
		return BackupIndex{}, false, ErrInvalidState
	}

	record, found, err := backupIndex(ctx, reader.transaction, identifier)
	if err != nil {
		return BackupIndex{}, false, err
	}

	return reader.backupResult(record, found)
}

// Actions returns every action for a transaction from the same stable snapshot.
func (reader *Reader) Actions(ctx context.Context, identifier TransactionID) ([]Action, error) {
	if reader == nil {
		return nil, ErrInvalidState
	}

	err := reader.requireValid()
	if err != nil {
		return nil, ErrInvalidState
	}

	records, err := actions(ctx, reader.transaction, identifier)
	if err != nil {
		return nil, err
	}

	return reader.actionResult(records)
}

// Close releases the stable snapshot and its retained filesystem capabilities.
func (reader *Reader) Close() error {
	if reader == nil || reader.closed {
		return ErrUnavailable
	}

	reader.closed = true
	if reader.database == nil && reader.transaction == nil && reader.anchor == nil {
		return nil
	}

	valid := reader.anchor.readerValid(reader.sidecars)
	transactionErr := reader.transaction.Rollback()
	databaseErr := reader.database.Close()
	anchorErr := reader.anchor.close()

	if !valid {
		return errors.Join(ErrInvalidState, transactionErr, databaseErr, anchorErr)
	}

	if transactionErr != nil || databaseErr != nil || anchorErr != nil {
		return errors.Join(ErrUnavailable, transactionErr, databaseErr, anchorErr)
	}

	return nil
}

func (reader *Reader) valid() bool {
	return reader != nil && !reader.closed && reader.database != nil && reader.transaction != nil &&
		reader.anchor != nil && reader.anchor.readerValid(reader.sidecars)
}

func (reader *Reader) requireValid() error {
	if !reader.valid() {
		return ErrInvalidState
	}

	return nil
}

func (reader *Reader) unresolvedResult(record Transaction, found bool) (Transaction, bool, error) {
	err := reader.requireValid()
	if err != nil {
		return Transaction{}, false, err
	}

	return record, found, nil
}

func (reader *Reader) appliedResult(record AppliedService, found bool) (AppliedService, bool, error) {
	err := reader.requireValid()
	if err != nil {
		return AppliedService{}, false, err
	}

	return record, found, nil
}

func (reader *Reader) backupResult(record BackupIndex, found bool) (BackupIndex, bool, error) {
	err := reader.requireValid()
	if err != nil {
		return BackupIndex{}, false, err
	}

	return record, found, nil
}

func (reader *Reader) actionResult(records []Action) ([]Action, error) {
	err := reader.requireValid()
	if err != nil {
		return nil, err
	}

	return records, nil
}

func classifyReaderOpen(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	if errors.Is(err, ErrUnavailable) {
		return err
	}

	return ErrInvalidState
}

func classifyReaderValidation(ctx context.Context) error {
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	return ErrInvalidState
}
