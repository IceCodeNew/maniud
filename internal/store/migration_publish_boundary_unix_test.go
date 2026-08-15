//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

//nolint:cyclop,funlen // The fault matrix keeps each publication phase visible in one test.
func TestPublishMigrationBackupContainsOperationFailures(t *testing.T) {
	t.Parallel()

	manifest, err := publishMigrationBackup(context.Background(), nil)
	if manifest != emptyMigrationBackupManifest() || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("nil snapshot publish = %#v, %v", manifest, err)
	}

	t.Run("invalid plan", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot := newMigrationSnapshot(t, database, anchor)
		snapshot.size = 0

		manifest, err := publishMigrationBackup(context.Background(), snapshot)
		if manifest != emptyMigrationBackupManifest() || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("invalid plan publish = %#v, %v", manifest, err)
		}

		assertNoMigrationTemporary(t, anchor.directoryPath)
	})

	t.Run("artifact before effect", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot := newMigrationSnapshot(t, database, anchor)

		manifest, err := publishMigrationBackupWithOps(context.Background(), snapshot, migrationPublishOps{
			publishArtifact: func(*migrationSnapshot, migrationBackupPlan) (bool, error) {
				return false, ErrUnavailable
			},
			publishManifest: publishMigrationManifest,
		})
		if manifest != emptyMigrationBackupManifest() || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("pre-effect failure = %#v, %v", manifest, err)
		}

		assertNoMigrationTemporary(t, anchor.directoryPath)
	})

	t.Run("artifact after effect", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot, plan := migrationPublicationFixture(t, database, anchor)
		removeMigrationPlanFiles(t, anchor, plan)

		manifest, err := publishMigrationBackupWithOps(context.Background(), snapshot, migrationPublishOps{
			publishArtifact: func(snapshot *migrationSnapshot, plan migrationBackupPlan) (bool, error) {
				established, publishErr := publishMigrationArtifact(snapshot, plan)
				if publishErr != nil {
					return established, publishErr
				}

				return true, ErrUnavailable
			},
			publishManifest: publishMigrationManifest,
		})
		if manifest != emptyMigrationBackupManifest() || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("post-effect failure = %#v, %v", manifest, err)
		}
	})

	t.Run("manifest", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot, plan := migrationPublicationFixture(t, database, anchor)
		removeMigrationPlanFiles(t, anchor, plan)

		manifest, err := publishMigrationBackupWithOps(context.Background(), snapshot, migrationPublishOps{
			publishArtifact: publishMigrationArtifact,
			publishManifest: func(*stateAnchor, migrationBackupPlan) (bool, error) {
				return false, ErrUnavailable
			},
		})
		if manifest != emptyMigrationBackupManifest() || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest failure = %#v, %v", manifest, err)
		}
	})

	t.Run("release", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot, plan := migrationPublicationFixture(t, database, anchor)
		removeMigrationPlanFiles(t, anchor, plan)

		manifestCalled := false

		manifest, err := publishMigrationBackupWithOps(context.Background(), snapshot, migrationPublishOps{
			publishArtifact: func(snapshot *migrationSnapshot, plan migrationBackupPlan) (bool, error) {
				established, publishErr := publishMigrationArtifact(snapshot, plan)
				requireNoError(t, publishErr)
				requireNoError(t, snapshot.file.Close())

				return established, nil
			},
			publishManifest: func(*stateAnchor, migrationBackupPlan) (bool, error) {
				manifestCalled = true

				return false, nil
			},
		})
		if manifest != emptyMigrationBackupManifest() || !errors.Is(err, ErrUnavailable) || !manifestCalled {
			t.Fatalf("release failure = %#v, %v, called=%t", manifest, err, manifestCalled)
		}
	})
}

//nolint:cyclop,funlen // The fault matrix keeps rename, identity, sync, and cleanup failures together.
func TestPublishMigrationArtifactContainsFilesystemFailures(t *testing.T) {
	t.Parallel()

	t.Run("rename", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)

		snapshot, plan := migrationPublicationFixture(t, database, anchor)
		defer func() { requireNoError(t, snapshot.Close()) }()

		established, err := publishMigrationArtifactWithOps(snapshot, plan, migrationArtifactPublishOps{
			rename:        func(int, string, string) error { return ErrUnavailable },
			syncDirectory: syncMigrationSnapshotDirectory,
		})
		if established || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("rename failure = %t, %v", established, err)
		}
	})

	t.Run("identity", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot, plan := migrationPublicationFixture(t, database, anchor)

		established, err := publishMigrationArtifactWithOps(snapshot, plan, migrationArtifactPublishOps{
			rename: func(directory int, source, destination string) error {
				renameErr := renameNoReplace(directory, source, destination)
				requireNoError(t, renameErr)
				requireNoError(t, unix.Unlinkat(directory, destination, 0))

				return nil
			},
			syncDirectory: syncMigrationSnapshotDirectory,
		})
		if !established || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("identity failure = %t, %v", established, err)
		}

		requireNoError(t, releaseMigrationSnapshot(snapshot))
	})

	t.Run("directory sync", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot, plan := migrationPublicationFixture(t, database, anchor)
		removeMigrationPlanFiles(t, anchor, plan)

		established, err := publishMigrationArtifactWithOps(snapshot, plan, migrationArtifactPublishOps{
			rename:        renameNoReplace,
			syncDirectory: func(int) error { return ErrUnavailable },
		})
		if !established || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("directory sync failure = %t, %v", established, err)
		}

		requireNoError(t, releaseMigrationSnapshot(snapshot))
	})

	t.Run("existing cleanup", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		first, plan := migrationPublicationFixture(t, database, anchor)
		removeMigrationPlanFiles(t, anchor, plan)

		established, err := publishMigrationArtifact(first, plan)
		if !established {
			t.Fatal("first artifact was not established")
		}

		requireNoError(t, err)
		requireNoError(t, releaseMigrationSnapshot(first))

		second := newMigrationSnapshot(t, database, anchor)
		requireNoError(t, second.file.Close())

		established, err = publishMigrationArtifact(second, plan)
		if !established || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("existing cleanup failure = %t, %v", established, err)
		}

		requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, second.name)))
	})

	if releaseMigrationSnapshot(nil) != nil {
		t.Fatal("nil migration snapshot release failed")
	}
}

//nolint:cyclop,funlen // The fault matrix keeps manifest publication phases visible in one test.
func TestPublishMigrationManifestContainsFilesystemFailures(t *testing.T) {
	t.Parallel()

	t.Run("open", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		_, plan := publishedMigrationArtifactFixture(t, database, anchor)

		established, err := publishMigrationManifestWithOps(anchor, plan, standardManifestOps(stringsReader("short")))
		if established || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest open failure = %t, %v", established, err)
		}
	})

	t.Run("write", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		_, plan := publishedMigrationArtifactFixture(t, database, anchor)
		operations := standardManifestOps(bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)))
		operations.write = func(*migrationManifestTemp, []byte) bool { return false }

		established, err := publishMigrationManifestWithOps(anchor, plan, operations)
		if established || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest write failure = %t, %v", established, err)
		}

		assertNoMigrationTemporary(t, anchor.directoryPath)
	})

	t.Run("rename", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		_, plan := publishedMigrationArtifactFixture(t, database, anchor)
		operations := standardManifestOps(bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)))
		operations.rename = func(int, string, string) error { return ErrUnavailable }

		established, err := publishMigrationManifestWithOps(anchor, plan, operations)
		if established || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest rename failure = %t, %v", established, err)
		}

		assertNoMigrationTemporary(t, anchor.directoryPath)
	})

	t.Run("identity", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		_, plan := publishedMigrationArtifactFixture(t, database, anchor)
		operations := standardManifestOps(bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)))
		operations.rename = func(directory int, source, destination string) error {
			renameErr := renameNoReplace(directory, source, destination)
			requireNoError(t, renameErr)
			requireNoError(t, unix.Unlinkat(directory, destination, 0))

			return nil
		}

		established, err := publishMigrationManifestWithOps(anchor, plan, operations)
		if !established || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("manifest identity failure = %t, %v", established, err)
		}
	})

	t.Run("directory sync", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		_, plan := publishedMigrationArtifactFixture(t, database, anchor)
		removeMigrationPlanFiles(t, anchor, plan)

		operations := standardManifestOps(bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)))
		operations.syncDirectory = func(int) error { return ErrUnavailable }

		established, err := publishMigrationManifestWithOps(anchor, plan, operations)
		if !established || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest sync failure = %t, %v", established, err)
		}
	})

	t.Run("release", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		_, plan := publishedMigrationArtifactFixture(t, database, anchor)
		removeMigrationPlanFiles(t, anchor, plan)
		temporary, err := openMigrationManifestTemp(
			anchor,
			bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)),
		)
		requireNoError(t, err)

		if !temporary.write(plan.content) {
			t.Fatal("manifest temporary write failed")
		}

		requireNoError(t, renameNoReplace(anchor.directory, temporary.name, plan.manifestName))
		temporary.name = plan.manifestName

		err = finishPublishedMigrationManifest(temporary, func(int) error {
			return temporary.file.Close()
		})
		if err == nil {
			t.Fatal("closed manifest descriptor released successfully")
		}
	})
}

//nolint:cyclop // The fault matrix exercises invalid anchors, collisions, identities, and nil receivers.
func TestMigrationManifestTempContainsFilesystemFailures(t *testing.T) {
	t.Parallel()

	anchor, _ := testMigrationDatabase(t)

	temporary, err := openMigrationManifestTemp(nil, bytes.NewReader(nil))
	if temporary != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("nil anchor manifest = %#v, %v", temporary, err)
	}

	nonce := make([]byte, migrationSnapshotNonceBytes)
	name := migrationManifestTempPrefix + stringsRepeatZeroNonce() + migrationManifestTempSuffix
	writeAnchoredFile(t, anchor.directory, name, "collision")

	temporary, err = openMigrationManifestTemp(anchor, bytes.NewReader(nonce))
	if temporary != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("manifest collision = %#v, %v", temporary, err)
	}

	requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, name)))

	descriptor, err := unix.Openat(
		anchor.directory,
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	requireNoError(t, err)
	requireNoError(t, unix.Fchmod(descriptor, 0o644))

	temporary, err = adoptMigrationManifestTemp(anchor, name, descriptor)
	if temporary != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("unsafe manifest temp = %#v, %v", temporary, err)
	}

	requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, name)))

	if (*migrationManifestTemp)(nil).Valid() || !errors.Is((*migrationManifestTemp)(nil).Close(), ErrUnavailable) ||
		(*migrationManifestTemp)(nil).release() != nil {
		t.Fatal("nil manifest temporary accepted an operation")
	}

	if (*migrationManifestTemp)(nil).write([]byte("content")) {
		t.Fatal("nil manifest temporary accepted a write")
	}
}

//nolint:funlen // The fault matrix keeps descriptor and durability cleanup failures together.
func TestMigrationManifestTempContainsWriteAndCleanupFailures(t *testing.T) {
	t.Parallel()

	t.Run("write", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)
		temporary, err := openMigrationManifestTemp(
			anchor,
			bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)),
		)
		requireNoError(t, err)
		requireNoError(t, temporary.file.Close())

		descriptor, err := unix.Openat(anchor.directory, temporary.name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		requireNoError(t, err)

		temporary.file = os.NewFile(uintptr(descriptor), temporary.name)

		if temporary.write([]byte("content")) {
			t.Fatal("read-only manifest accepted a write")
		}

		requireNoError(t, temporary.Close())
	})

	t.Run("identity", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)
		temporary, err := openMigrationManifestTemp(
			anchor,
			bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)),
		)
		requireNoError(t, err)

		entry := filepath.Join(anchor.directoryPath, temporary.name)
		requireNoError(t, os.Rename(entry, entry+".moved"))
		requireNoError(t, os.WriteFile(entry, []byte("replacement"), privateFileMode))

		if !errors.Is(temporary.Close(), ErrInvalidState) {
			t.Fatal("replaced manifest temporary was removed")
		}

		requireNoError(t, os.Remove(entry))
		requireNoError(t, os.Remove(entry+".moved"))
	})

	t.Run("unlink", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)
		temporary, err := openMigrationManifestTemp(
			anchor,
			bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)),
		)
		requireNoError(t, err)

		err = temporary.closeWith(
			func(int, string) error { return ErrUnavailable },
			syncMigrationSnapshotDirectory,
		)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest unlink failure = %v", err)
		}

		requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, temporary.name)))
	})

	t.Run("sync", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)
		temporary, err := openMigrationManifestTemp(
			anchor,
			bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)),
		)
		requireNoError(t, err)

		err = temporary.closeWith(
			unlinkMigrationSnapshot,
			func(int) error { return ErrUnavailable },
		)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest directory sync failure = %v", err)
		}
	})

	t.Run("release", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)
		temporary, err := openMigrationManifestTemp(
			anchor,
			bytes.NewReader(make([]byte, migrationSnapshotNonceBytes)),
		)
		requireNoError(t, err)
		requireNoError(t, temporary.file.Close())

		releaseErr := temporary.release()
		if releaseErr == nil {
			t.Fatal("closed manifest descriptor released successfully")
		}

		requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, temporary.name)))
	})
}

func TestOpenAnchoredPrivateFileRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()

	anchor, _ := testMigrationDatabase(t)
	if file, _, valid := openAnchoredPrivateFile(nil, "missing"); valid || file != nil {
		t.Fatal("nil anchor opened a private file")
	}

	if file, _, valid := openAnchoredPrivateFile(anchor, "missing"); valid || file != nil {
		t.Fatal("missing private file opened")
	}

	emptyPlan := migrationBackupPlan{
		manifest:     emptyMigrationBackupManifest(),
		artifactName: "missing-artifact",
		manifestName: "missing-manifest",
		content:      nil,
	}
	if existingMigrationArtifactMatches(anchor, emptyPlan) || existingMigrationManifestMatches(anchor, emptyPlan) {
		t.Fatal("missing migration pair matched a plan")
	}

	writeAnchoredFile(t, anchor.directory, "unsafe", "content")
	unsafePath := filepath.Join(anchor.directoryPath, "unsafe")
	requireNoError(t, os.Chmod(unsafePath, 0o644)) //nolint:gosec // Deliberately unsafe fixture.

	if file, _, valid := openAnchoredPrivateFile(anchor, "unsafe"); valid || file != nil {
		t.Fatal("unsafe private file opened")
	}
}

func migrationPublicationFixture(
	t *testing.T,
	database *sql.DB,
	anchor *stateAnchor,
) (*migrationSnapshot, migrationBackupPlan) {
	t.Helper()

	snapshot := newMigrationSnapshot(t, database, anchor)

	plan, valid := planMigrationBackup(
		anchor.databaseName,
		snapshot.sourceSchema,
		snapshot.targetSchema,
		snapshot.size,
		snapshot.digest,
	)
	if !valid {
		t.Fatal("planMigrationBackup() rejected snapshot")
	}

	return snapshot, plan
}

func publishedMigrationArtifactFixture(
	t *testing.T,
	database *sql.DB,
	anchor *stateAnchor,
) (*migrationSnapshot, migrationBackupPlan) {
	t.Helper()

	snapshot, plan := migrationPublicationFixture(t, database, anchor)
	removeMigrationPlanFiles(t, anchor, plan)

	established, err := publishMigrationArtifact(snapshot, plan)
	if !established {
		t.Fatal("migration artifact was not established")
	}

	requireNoError(t, err)
	requireNoError(t, releaseMigrationSnapshot(snapshot))

	return snapshot, plan
}

func removeMigrationPlanFiles(t *testing.T, anchor *stateAnchor, plan migrationBackupPlan) {
	t.Helper()

	t.Cleanup(func() {
		for _, name := range []string{plan.manifestName, plan.artifactName} {
			err := os.Remove(filepath.Join(anchor.directoryPath, name))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Error(err)
			}
		}
	})
}

func standardManifestOps(random *bytes.Reader) migrationManifestPublishOps {
	return migrationManifestPublishOps{
		random:        random,
		write:         (*migrationManifestTemp).write,
		rename:        renameNoReplace,
		syncDirectory: syncMigrationSnapshotDirectory,
	}
}

func stringsReader(value string) *bytes.Reader {
	return bytes.NewReader([]byte(value))
}

func stringsRepeatZeroNonce() string {
	return "00000000000000000000000000000000"
}
