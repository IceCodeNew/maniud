package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

const pythonBaselineCommit = "49b80d9afea20a8b247d2d617478beef3de9d97d"

type cliCorpus struct {
	SchemaVersion  int `json:"schema_version"`
	PythonBaseline struct {
		Repository string `json:"repository"`
		Commit     string `json:"commit"`
	} `json:"python_baseline"`
	Cases []cliCase `json:"cases"`
}

type cliCase struct {
	ID           string   `json:"id"`
	Argv         []string `json:"argv"`
	Accepted     bool     `json:"accepted"`
	DifferenceID string   `json:"difference_id"`
}

type differenceLedger struct {
	SchemaVersion        int    `json:"schema_version"`
	PythonBaselineCommit string `json:"python_baseline_commit"`
	Differences          []struct {
		ID string `json:"id"`
	} `json:"differences"`
}

func TestCLICorpus(t *testing.T) {
	t.Parallel()

	var corpus cliCorpus

	readContract(t, "cli-v1.json", &corpus)
	validateCorpusMetadata(t, corpus)
	differenceIDs := validateCorpusCases(t, corpus.Cases)

	var ledger differenceLedger

	readContract(t, "approved-differences-v1.json", &ledger)
	validateDifferenceLedger(t, ledger, differenceIDs)
}

func validateCorpusMetadata(t *testing.T, corpus cliCorpus) {
	t.Helper()

	if corpus.SchemaVersion != 1 {
		t.Fatalf("CLI corpus schema = %d, want 1", corpus.SchemaVersion)
	}

	if corpus.PythonBaseline.Repository != "https://github.com/IceCodeNew/deprecate-maniud" {
		t.Fatalf("CLI corpus repository = %q", corpus.PythonBaseline.Repository)
	}

	if corpus.PythonBaseline.Commit != pythonBaselineCommit {
		t.Fatalf("CLI corpus commit = %q, want %q", corpus.PythonBaseline.Commit, pythonBaselineCommit)
	}
}

func validateCorpusCases(t *testing.T, cases []cliCase) map[string]bool {
	t.Helper()

	seen := make(map[string]bool, len(cases))
	differenceIDs := make(map[string]bool)

	for _, test := range cases {
		if seen[test.ID] {
			t.Fatalf("duplicate CLI corpus case %q", test.ID)
		}

		seen[test.ID] = true
		_, err := parse(test.Argv)

		if (err == nil) != test.Accepted {
			t.Fatalf("parse(%q) accepted = %t, want %t", test.Argv, err == nil, test.Accepted)
		}

		if test.DifferenceID != "" {
			differenceIDs[test.DifferenceID] = true
		}
	}

	return differenceIDs
}

func validateDifferenceLedger(t *testing.T, ledger differenceLedger, differenceIDs map[string]bool) {
	t.Helper()

	if ledger.SchemaVersion != 1 || ledger.PythonBaselineCommit != pythonBaselineCommit {
		t.Fatalf("approved-difference ledger metadata is invalid")
	}

	if len(ledger.Differences) != len(differenceIDs) {
		t.Fatalf("approved differences = %d, corpus references = %d", len(ledger.Differences), len(differenceIDs))
	}

	for _, difference := range ledger.Differences {
		if !differenceIDs[difference.ID] {
			t.Fatalf("approved difference %q has no corpus case", difference.ID)
		}
	}
}

func readContract(t *testing.T, name string, target any) {
	t.Helper()

	path := filepath.Join("..", "..", "testdata", "contracts", name)
	// The caller supplies one of two repository-owned contract filenames.
	//nolint:gosec // No external value can select a path.
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}

	decodeErr := json.Unmarshal(contents, target)
	if decodeErr != nil {
		t.Fatalf("decode %s: %v", path, decodeErr)
	}
}
