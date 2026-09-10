package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

type schemaUpgradeCancellationQuery struct {
	journalQueryer

	cancel context.CancelFunc
	calls  int
}

func (query *schemaUpgradeCancellationQuery) QueryRowContext(
	ctx context.Context, statement string, arguments ...any,
) *sql.Row {
	query.calls++
	if query.calls == 2 {
		query.cancel()
	}

	return query.journalQueryer.QueryRowContext(ctx, statement, arguments...)
}

func TestHistoricalSchemaCancellationBetweenVersionProbes(t *testing.T) {
	t.Parallel()
	path, _ := releasedV1State(t)
	database := testDatabase(t, sqliteURI(path))
	_, err := database.ExecContext(t.Context(), "UPDATE schema_version SET version = 2")
	requireNoError(t, err)
	before, err := readSchemaFactsAtVersion(t.Context(), database, 2)
	requireNoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	query := &schemaUpgradeCancellationQuery{journalQueryer: database, cancel: cancel}
	if _, err = readVersion1Query(ctx, query); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled compatibility proof = %v", err)
	}
	after, err := readSchemaFactsAtVersion(t.Context(), database, 2)
	requireNoError(t, err)
	if before != after {
		t.Fatal("cancelled compatibility proof changed schema")
	}
}

//nolint:cyclop,funlen // Verify read-only, migration, and reopen against each frozen schema.
func TestEarlierRepositorySchemasPreserveHealthAndIdentity(t *testing.T) {
	t.Parallel()
	// Frozen from bottom 3472054 and the previously published top 4ef54a6.
	for _, test := range []struct {
		fixture string
		health  string
		want    AppliedService
	}{
		{"schema-v2.sql", "", AppliedService{HealthcheckUnknown: true}},
		{"schema-health-v1.sql", ", 0", AppliedService{}},
		{"schema-health-v1.sql", ", 1", AppliedService{Healthcheck: true}},
	} {
		t.Run(test.fixture+test.health, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(privateTempDir(t), "state.db")
			requireNoError(t, os.WriteFile(path, nil, 0o600))
			requireNoError(t, os.WriteFile(path+".lock", nil, 0o600))
			schema, err := os.ReadFile(filepath.Join("testdata", test.fixture))
			requireNoError(t, err)
			database := testDatabase(t, sqliteURI(path))
			_, err = database.ExecContext(t.Context(), string(schema))
			requireNoError(t, err)
			service, _ := serviceIdentity("project", "api")
			identifier := TransactionID{71}
			digest := domain.Digest{83}
			scope := domain.Digest{97}
			location := domain.Digest{101}
			_, err = database.ExecContext(t.Context(), "INSERT INTO writer_leases VALUES (?, 5, NULL)", service[:])
			requireNoError(t, err)
			_, err = database.ExecContext(t.Context(), `INSERT INTO journal_transactions VALUES
(?, ?, 'bootstrap', 'succeeded', 'docker', ?, ?, ?, 1, ?, ?, NULL, NULL)`,
				identifier[:], service[:], digest[:], digest[:], digest[:], scope[:], location[:])
			requireNoError(t, err)
			_, err = database.ExecContext(t.Context(),
				//nolint:gosec // Static schema-specific fixture suffix.
				"INSERT INTO applied_services VALUES (?, ?, 'predecessor', ?, ?, ?, ?, ?"+test.health+")",
				service[:], identifier[:], digest[:], digest[:], digest[:], digest[:], digest[:])
			requireNoError(t, err)
			requireNoError(t, database.Close())
			before, err := os.ReadFile(path) //nolint:gosec // Test-owned database.
			requireNoError(t, err)
			reader := requireOpenReader(t, path)
			applied, found, err := reader.AppliedService(t.Context(), "project", "api")
			requireNoError(t, err)
			if !found || applied.Healthcheck != test.want.Healthcheck ||
				applied.HealthcheckUnknown != test.want.HealthcheckUnknown {
				t.Fatalf("read-only historical health = %#v", applied)
			}
			requireNoError(t, reader.Close())
			after, err := os.ReadFile(path) //nolint:gosec // Test-owned database.
			requireNoError(t, err)
			if !bytes.Equal(before, after) {
				t.Fatal("historical read-only inspection wrote the database")
			}
			for range 2 {
				state := openJournalStore(t, path)
				got, exists, queryErr := state.AppliedService(t.Context(), "project", "api")
				requireNoError(t, queryErr)
				if !exists || got != applied {
					t.Fatalf("migration changed applied generation: %#v; want %#v", got, applied)
				}
				record, queryErr := state.Transaction(t.Context(), identifier)
				requireNoError(t, queryErr)
				if !record.HasRepository || record.RepositoryVersion != 1 ||
					record.RepositoryScopeDigest != scope || record.RepositoryLocationDigest != location {
					t.Fatalf("migration changed repository proof: %#v", record)
				}
				requireNoError(t, state.Close())
			}
		})
	}
}
