package store

import (
	"crypto/sha256"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const migrationManifestTestDatabase = "state.db"

func TestPlanMigrationBackupCreatesCanonicalManifest(t *testing.T) {
	t.Parallel()

	digest := sha256.Sum256([]byte("snapshot"))

	plan, valid := planMigrationBackup(migrationManifestTestDatabase, 1, 2, 4096, digest)
	if !valid {
		t.Fatal("planMigrationBackup() rejected valid state")
	}

	wantArtifact := "state.db.schema-1-to-2.sha256-" + plan.manifest.SHA256 + ".sqlite"
	if plan.artifactName != wantArtifact ||
		plan.manifestName != strings.TrimSuffix(wantArtifact, ".sqlite")+".manifest.json" ||
		plan.manifest.Artifact != wantArtifact {
		t.Fatalf("migration backup names = %#v", plan)
	}

	parsed, valid := parseMigrationBackupManifest(
		strings.NewReader(string(plan.content)),
		migrationManifestTestDatabase,
	)
	if !valid || parsed != plan.manifest || plan.content[len(plan.content)-1] != '\n' {
		t.Fatalf("parseMigrationBackupManifest() = %#v, %t", parsed, valid)
	}
}

func TestPlanMigrationBackupRejectsInvalidState(t *testing.T) {
	t.Parallel()

	digest := sha256.Sum256([]byte("snapshot"))

	for _, test := range []struct {
		name     string
		database string
		source   int
		target   int
		size     int64
	}{
		{name: "empty database", database: "", source: 1, target: 2, size: 1},
		{name: "path database", database: "dir/state.db", source: 1, target: 2, size: 1},
		{name: "zero source", database: migrationManifestTestDatabase, source: 0, target: 1, size: 1},
		{name: "skipped target", database: migrationManifestTestDatabase, source: 1, target: 3, size: 1},
		{name: "overflow target", database: migrationManifestTestDatabase, source: math.MaxInt, target: math.MinInt, size: 1},
		{name: "empty artifact", database: migrationManifestTestDatabase, source: 1, target: 2, size: 0},
	} {
		plan, valid := planMigrationBackup(test.database, test.source, test.target, test.size, digest)
		if valid || plan.manifest != emptyMigrationBackupManifest() || plan.artifactName != "" ||
			plan.manifestName != "" || plan.content != nil {
			t.Fatalf("planMigrationBackup(%s) = %#v, %t", test.name, plan, valid)
		}
	}
}

func TestParseMigrationBackupManifestRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	digest := sha256.Sum256([]byte("snapshot"))

	plan, valid := planMigrationBackup(migrationManifestTestDatabase, 1, 2, 4096, digest)
	if !valid {
		t.Fatal("planMigrationBackup() rejected valid state")
	}

	for _, value := range []string{
		`{"kind":"maniud.sqlite-migration-backup","kind":"duplicate"}`,
		`{"unknown":true}`,
		strings.Repeat(" ", migrationManifestMaxBytes+1),
		`{`,
	} {
		manifest, valid := parseMigrationBackupManifest(strings.NewReader(value), migrationManifestTestDatabase)
		if valid || manifest != emptyMigrationBackupManifest() {
			t.Fatalf("parseMigrationBackupManifest(invalid) = %#v, %t", manifest, valid)
		}
	}

	mutations := []func(*migrationBackupManifest){
		func(manifest *migrationBackupManifest) { manifest.Kind = "unknown" },
		func(manifest *migrationBackupManifest) { manifest.FormatVersion++ },
		func(manifest *migrationBackupManifest) { manifest.SHA256 = strings.ToUpper(manifest.SHA256) },
		func(manifest *migrationBackupManifest) { manifest.SHA256 = "00" },
		func(manifest *migrationBackupManifest) { manifest.Artifact = "other.sqlite" },
	}

	for _, mutate := range mutations {
		manifest := plan.manifest
		mutate(&manifest)

		content, err := json.Marshal(manifest)
		requireNoError(t, err)

		parsed, accepted := parseMigrationBackupManifest(
			strings.NewReader(string(content)),
			migrationManifestTestDatabase,
		)
		if accepted || parsed != manifest {
			t.Fatalf("parseMigrationBackupManifest(mutation) = %#v, %t", parsed, accepted)
		}
	}
}

func emptyMigrationBackupManifest() migrationBackupManifest {
	return migrationBackupManifest{
		Kind:          "",
		FormatVersion: 0,
		SourceSchema:  0,
		TargetSchema:  0,
		Artifact:      "",
		Size:          0,
		SHA256:        "",
	}
}
