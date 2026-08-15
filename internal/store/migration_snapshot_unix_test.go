//go:build linux || darwin

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateMigrationSnapshotBuildsValidatedDigest(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)

	snapshot, err := createMigrationSnapshot(
		context.Background(),
		database,
		anchor,
		currentSchemaVersion,
		currentSchemaVersion+1,
	)
	if err != nil || snapshot == nil {
		t.Fatalf("createMigrationSnapshot() = %#v, %v", snapshot, err)
	}

	if !snapshot.Valid() || snapshot.sourceSchema != currentSchemaVersion ||
		snapshot.targetSchema != currentSchemaVersion+1 || snapshot.size <= 0 {
		t.Fatalf("migration snapshot = %#v", snapshot)
	}

	content := []byte(readAnchoredFile(t, anchor.directory, snapshot.name))
	wantDigest := sha256.Sum256(content)

	if int64(len(content)) != snapshot.size || snapshot.digest != wantDigest {
		t.Fatalf("snapshot measurement = %d, %x", snapshot.size, snapshot.digest)
	}

	name := snapshot.name
	requireNoError(t, snapshot.Close())

	_, err = os.Lstat(filepath.Join(anchor.directoryPath, name))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("closed snapshot entry error = %v", err)
	}
}

func TestCreateMigrationSnapshotRejectsInvalidContract(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)

	for _, test := range []struct {
		name      string
		cancelled bool
		database  bool
		anchor    bool
		source    int
		target    int
	}{
		{name: "cancelled", cancelled: true, database: true, anchor: true, source: 1, target: 2},
		{name: "nil database", cancelled: false, database: false, anchor: true, source: 1, target: 2},
		{name: "nil anchor", cancelled: false, database: true, anchor: false, source: 1, target: 2},
		{name: "invalid source", cancelled: false, database: true, anchor: true, source: 0, target: 1},
		{name: "nonsequential target", cancelled: false, database: true, anchor: true, source: 1, target: 3},
		{name: "overflow target", cancelled: false, database: true, anchor: true, source: math.MaxInt, target: math.MinInt},
	} {
		testContext := context.Background()
		if test.cancelled {
			cancelledContext, cancel := context.WithCancel(testContext)
			cancel()

			testContext = cancelledContext
		}

		databaseArgument := database
		if !test.database {
			databaseArgument = nil
		}

		anchorArgument := anchor
		if !test.anchor {
			anchorArgument = nil
		}

		snapshot, err := createMigrationSnapshot(
			testContext,
			databaseArgument,
			anchorArgument,
			test.source,
			test.target,
		)
		if snapshot != nil || !errors.Is(err, ErrInvalidState) && !errors.Is(err, context.Canceled) {
			t.Fatalf("createMigrationSnapshot(%s) = %#v, %v", test.name, snapshot, err)
		}
	}

	requireNoError(t, anchor.unlock())

	snapshot, err := createMigrationSnapshot(context.Background(), database, anchor, 1, 2)
	if snapshot != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("createMigrationSnapshot(unlocked) = %#v, %v", snapshot, err)
	}
}

func TestCreateMigrationSnapshotCleansFailedBackup(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)

	snapshot, err := createMigrationSnapshotWithBackup(
		context.Background(),
		database,
		anchor,
		1,
		2,
		func(context.Context, *sql.DB, string) error {
			return ErrUnavailable
		},
	)
	if snapshot != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("createMigrationSnapshot(closed database) = %#v, %v", snapshot, err)
	}

	entries, err := os.ReadDir(anchor.directoryPath)
	requireNoError(t, err)

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), migrationSnapshotPrefix) {
			t.Fatalf("failed backup left %q", entry.Name())
		}
	}
}

type migrationSnapshotBoundaryCase struct {
	name      string
	configure func(migrationSnapshotOps) migrationSnapshotOps
	want      error
}

func TestCreateMigrationSnapshotContainsBoundaryFailures(t *testing.T) {
	t.Parallel()

	for _, test := range migrationSnapshotBoundaryCases() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			anchor, database := testMigrationDatabase(t)

			snapshot, err := createMigrationSnapshotWithOps(
				context.Background(),
				database,
				anchor,
				1,
				2,
				test.configure(standardMigrationSnapshotOps()),
			)
			if snapshot != nil || !errors.Is(err, test.want) {
				t.Fatalf("createMigrationSnapshotWithOps() = %#v, %v", snapshot, err)
			}

			assertNoMigrationSnapshot(t, anchor.directoryPath)
		})
	}
}

func migrationSnapshotBoundaryCases() []migrationSnapshotBoundaryCase {
	return []migrationSnapshotBoundaryCase{
		{
			name: "random",
			configure: func(operations migrationSnapshotOps) migrationSnapshotOps {
				operations.random = strings.NewReader("short")

				return operations
			},
			want: ErrUnavailable,
		},
		{
			name: "validation",
			configure: func(operations migrationSnapshotOps) migrationSnapshotOps {
				operations.backup = writeInvalidSnapshot
				operations.validate = func(context.Context, string, int) bool { return false }

				return operations
			},
			want: ErrInvalidState,
		},
		{
			name: "sync",
			configure: func(operations migrationSnapshotOps) migrationSnapshotOps {
				operations.backup = writeInvalidSnapshot
				operations.validate = func(context.Context, string, int) bool { return true }
				operations.sync = func(*os.File) error { return ErrUnavailable }

				return operations
			},
			want: ErrUnavailable,
		},
		{
			name: "empty measurement",
			configure: func(operations migrationSnapshotOps) migrationSnapshotOps {
				operations.backup = func(context.Context, *sql.DB, string) error { return nil }
				operations.validate = func(context.Context, string, int) bool { return true }

				return operations
			},
			want: ErrInvalidState,
		},
	}
}

func writeInvalidSnapshot(_ context.Context, _ *sql.DB, path string) error {
	err := os.WriteFile(path, []byte("invalid"), privateFileMode)
	if err != nil {
		return fmt.Errorf("write invalid snapshot: %w", err)
	}

	return nil
}

func assertNoMigrationSnapshot(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	requireNoError(t, err)

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), migrationSnapshotPrefix) {
			t.Fatalf("operation left %q", entry.Name())
		}
	}
}

func TestMigrationSnapshotValidationRejectsWrongState(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)

	snapshot, err := createMigrationSnapshot(context.Background(), database, anchor, 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	entry := filepath.Join(anchor.directoryPath, snapshot.name)

	t.Cleanup(func() {
		if snapshot.file != nil {
			requireNoError(t, snapshot.Close())

			return
		}

		requireNoError(t, os.Remove(entry))
	})

	if validMigrationSnapshot(context.Background(), platformEntryPath(anchor, snapshot.name), 2) {
		t.Fatal("snapshot accepted the wrong source schema")
	}

	closed := testDatabase(t, "file::memory:")
	requireNoError(t, closed.Close())

	if validMigrationSnapshotDatabase(context.Background(), closed, 1) {
		t.Fatal("closed database passed snapshot validation")
	}

	requireNoError(t, snapshot.file.Close())

	if _, _, valid := snapshot.measure(); valid {
		t.Fatal("closed snapshot produced a digest")
	}

	snapshot.file = nil

	if (*migrationSnapshot)(nil).Valid() || !errors.Is((*migrationSnapshot)(nil).Close(), ErrUnavailable) {
		t.Fatal("nil migration snapshot is valid or closed successfully")
	}
}

func TestClassifyMigrationSnapshotContainsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := classifyMigrationSnapshot(ctx)
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("classifyMigrationSnapshot(cancelled) = %v", err)
	}

	if !errors.Is(classifyMigrationSnapshot(context.Background()), ErrInvalidState) {
		t.Fatal("classifyMigrationSnapshot(invalid) did not fail closed")
	}
}

func testMigrationDatabase(t *testing.T) (*stateAnchor, *sql.DB) {
	t.Helper()

	anchor, database := testAnchoredDatabase(t)
	requireNoError(t, ready(context.Background(), database))
	requireNoError(t, ensureSchema(context.Background(), database))

	t.Cleanup(func() {
		closeErr := database.Close()
		if closeErr != nil {
			t.Error(closeErr)
		}

		requireNoError(t, anchor.close())
	})

	return anchor, database
}
