//go:build linux || darwin

package store

import (
	"io"
	"os"
	"strings"
)

const (
	migrationEntryUnrelated = iota
	migrationEntryArtifact
	migrationEntryManifest
)

func discoverMigrationBackup(anchor *stateAnchor) (*migrationBackupPair, bool, error) {
	if anchor == nil || !anchor.locked || !anchor.valid() {
		return nil, false, ErrInvalidState
	}

	names, err := migrationDirectoryNames(anchor)

	return discoverMigrationBackupFromNames(anchor, names, err)
}

func discoverMigrationBackupFromNames(
	anchor *stateAnchor,
	names []string,
	namesErr error,
) (*migrationBackupPair, bool, error) {
	if namesErr != nil {
		return nil, false, namesErr
	}

	artifactName, manifestName, found, err := migrationBackupNames(names, anchor.databaseName)
	if err != nil || !found {
		return nil, found, err
	}

	manifest, err := readDiscoveredMigrationManifest(anchor, manifestName)
	if err != nil {
		return nil, false, err
	}

	plan, valid := planExistingMigrationBackup(anchor.databaseName, manifest)
	if !valid || !migrationPlanNamesMatch(plan, artifactName, manifestName) {
		return nil, false, ErrInvalidState
	}

	pair, err := openMigrationBackupPair(anchor, manifest)

	return pair, err == nil, err
}

func migrationPlanNamesMatch(plan migrationBackupPlan, artifactName, manifestName string) bool {
	return plan.artifactName == artifactName && plan.manifestName == manifestName
}

func migrationBackupNames(names []string, databaseName string) (string, string, bool, error) {
	artifactName := ""
	manifestName := ""
	prefix := databaseName + ".schema-"

	for _, name := range names {
		kind, err := migrationEntryType(name, prefix)
		if err != nil {
			return "", "", false, err
		}

		err = assignMigrationEntry(kind, name, &artifactName, &manifestName)
		if err != nil {
			return "", "", false, err
		}
	}

	if artifactName == "" && manifestName == "" {
		return "", "", false, nil
	}

	if artifactName == "" || manifestName == "" {
		return "", "", false, ErrInvalidState
	}

	return artifactName, manifestName, true, nil
}

func assignMigrationEntry(kind int, name string, artifactName, manifestName *string) error {
	switch kind {
	case migrationEntryArtifact:
		if *artifactName != "" {
			return ErrInvalidState
		}

		*artifactName = name
	case migrationEntryManifest:
		if *manifestName != "" {
			return ErrInvalidState
		}

		*manifestName = name
	}

	return nil
}

func migrationEntryType(name, prefix string) (int, error) {
	if migrationTemporaryName(name) {
		return migrationEntryUnrelated, ErrInvalidState
	}

	if !strings.HasPrefix(name, prefix) {
		return migrationEntryUnrelated, nil
	}

	switch {
	case strings.HasSuffix(name, ".manifest.json"):
		return migrationEntryManifest, nil
	case strings.HasSuffix(name, ".sqlite"):
		return migrationEntryArtifact, nil
	default:
		return migrationEntryUnrelated, ErrInvalidState
	}
}

func migrationDirectoryNames(anchor *stateAnchor) ([]string, error) {
	return migrationDirectoryNamesWithRead(anchor, os.ReadDir)
}

func migrationDirectoryNamesWithRead(
	anchor *stateAnchor,
	readDirectory func(string) ([]os.DirEntry, error),
) ([]string, error) {
	if !anchor.validDirectory() {
		return nil, ErrInvalidState
	}

	entries, err := readDirectory(platformEntryPath(anchor, "."))
	if err != nil {
		return nil, ErrUnavailable
	}

	if !anchor.valid() {
		return nil, ErrInvalidState
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}

	return names, nil
}

func migrationTemporaryName(name string) bool {
	return strings.HasPrefix(name, migrationSnapshotPrefix)
}

func readDiscoveredMigrationManifest(
	anchor *stateAnchor,
	name string,
) (migrationBackupManifest, error) {
	return readDiscoveredMigrationManifestWithClose(anchor, name, (*os.File).Close)
}

func readDiscoveredMigrationManifestWithClose(
	anchor *stateAnchor,
	name string,
	closeFile func(*os.File) error,
) (migrationBackupManifest, error) {
	file, identity, valid := openAnchoredPrivateFile(anchor, name)
	if !valid {
		return migrationBackupManifest{}, ErrInvalidState
	}

	reader := io.NewSectionReader(file, 0, migrationManifestMaxBytes+1)
	manifest, valid := parseMigrationBackupManifest(reader, anchor.databaseName)
	plan, planned := planExistingMigrationBackup(anchor.databaseName, manifest)
	matches := valid && planned && plan.manifestName == name &&
		migrationManifestFileMatches(anchor, plan, file, identity)

	closeErr := closeFile(file)
	if closeErr != nil {
		return migrationBackupManifest{}, ErrUnavailable
	}

	if !matches {
		return migrationBackupManifest{}, ErrInvalidState
	}

	return manifest, nil
}
