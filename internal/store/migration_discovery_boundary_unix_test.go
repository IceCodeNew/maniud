//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverMigrationBackupContainsBoundaryFailures(t *testing.T) {
	t.Parallel()

	pair, found, err := discoverMigrationBackup(nil)
	if pair != nil || found || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("discoverMigrationBackup(nil) = %#v, %t, %v", pair, found, err)
	}

	anchor, _ := testMigrationDatabase(t)

	pair, found, err = discoverMigrationBackupFromNames(anchor, nil, ErrUnavailable)
	if pair != nil || found || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("discoverMigrationBackupFromNames() = %#v, %t, %v", pair, found, err)
	}
}

func TestMigrationDirectoryNamesRejectsUnavailableAndChangedAnchor(t *testing.T) {
	t.Parallel()

	t.Run("invalid descriptor", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)
		directory := anchor.directory
		anchor.directory = -1

		_, err := migrationDirectoryNames(anchor)
		anchor.directory = directory

		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("migrationDirectoryNames() = %v", err)
		}
	})

	t.Run("read failure", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)

		_, err := migrationDirectoryNamesWithRead(
			anchor,
			func(string) ([]os.DirEntry, error) {
				return nil, os.ErrPermission
			},
		)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("migrationDirectoryNamesWithRead() = %v", err)
		}
	})

	t.Run("changed identity", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)
		identity := anchor.directoryID

		_, err := migrationDirectoryNamesWithRead(
			anchor,
			func(path string) ([]os.DirEntry, error) {
				entries, readErr := os.ReadDir(path)
				requireNoError(t, readErr)

				anchor.directoryID = fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}

				return entries, nil
			},
		)
		anchor.directoryID = identity

		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("migrationDirectoryNamesWithRead() = %v", err)
		}
	})
}

func TestDiscoverMigrationBackupRejectsMismatchedPairNames(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot, plan := migrationPublicationFixture(t, database, anchor)
	requireNoError(t, snapshot.Close())

	wrongArtifact := migrationArtifactName(
		anchor.databaseName,
		plan.manifest.SourceSchema,
		plan.manifest.TargetSchema,
		strings.Repeat("f", 64),
	)
	writePrivateMigrationFixture(t, anchor, wrongArtifact, "wrong artifact")
	writePrivateMigrationFixture(t, anchor, plan.manifestName, string(plan.content))

	pair, found, err := discoverMigrationBackup(anchor)
	if pair != nil || found || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("discoverMigrationBackup() = %#v, %t, %v", pair, found, err)
	}
}

func TestReadDiscoveredMigrationManifestContainsOpenAndCloseFailures(t *testing.T) {
	t.Parallel()

	t.Run("unsafe entry", func(t *testing.T) {
		t.Parallel()

		anchor, _ := testMigrationDatabase(t)
		name := anchor.databaseName + ".schema-unsafe.manifest.json"
		requireNoError(t, os.Symlink("missing", filepath.Join(anchor.directoryPath, name)))

		_, err := readDiscoveredMigrationManifest(anchor, name)
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("readDiscoveredMigrationManifest() = %v", err)
		}
	})

	t.Run("close", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot := newMigrationSnapshot(t, database, anchor)
		manifest, err := publishMigrationBackup(context.Background(), snapshot)
		requireNoError(t, err)

		plan, valid := planExistingMigrationBackup(anchor.databaseName, manifest)
		if !valid {
			t.Fatal("published manifest has no valid plan")
		}

		_, err = readDiscoveredMigrationManifestWithClose(
			anchor,
			plan.manifestName,
			func(file *os.File) error {
				_ = file.Close()

				return ErrUnavailable
			},
		)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("readDiscoveredMigrationManifestWithClose() = %v", err)
		}
	})
}

func TestAssignMigrationEntryRejectsDuplicates(t *testing.T) {
	t.Parallel()

	artifactName := ""
	manifestName := "first"

	err := assignMigrationEntry(migrationEntryManifest, "second", &artifactName, &manifestName)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("assignMigrationEntry(duplicate manifest) = %v", err)
	}

	artifactName = "first"
	manifestName = ""

	err = assignMigrationEntry(migrationEntryArtifact, "second", &artifactName, &manifestName)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("assignMigrationEntry(duplicate artifact) = %v", err)
	}
}
