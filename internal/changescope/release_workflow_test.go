package changescope

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleasePreflightWritesRecognizedGoVersionManifest(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	// Extract the existing literal shell block; missing or changed delimiters fail the output assertions.
	_, revision, _ := strings.Cut(string(contents), "        id: revision\n")
	_, preflight, _ := strings.Cut(revision, "        run: |\n")
	preflight, _, _ = strings.Cut(preflight, "\n      - name:")
	preflight = strings.TrimPrefix(strings.ReplaceAll(preflight, "\n          ", "\n"), "          ")
	_, versionFile, _ := strings.Cut(string(contents), "          go-version-file: ")
	versionFile, _, _ = strings.Cut(versionFile, "\n")
	const readOnlyFixtures = `
gh() {
  case "$*" in
    */git/ref/heads/master*) printf '%s\n' "$GITHUB_SHA" ;;
    */checks.yml/runs*) printf '123\t%s\tpush\tsuccess\n' "$GITHUB_SHA" ;;
    *) return 91 ;;
  esac
}
git() {
  case "$*" in
    "show -s --format=%s $GITHUB_SHA") printf 'fixture subject\n' ;;
    "show ${GITHUB_SHA}:go.mod") printf '%s' "$FIXTURE_MOD" ;;
    *) return 92 ;;
  esac
}
`
	directory := t.TempDir()
	const manifest = "module example.test/release\n\ngo 1.27.0\n"
	//nolint:gosec // Execute repository-owned YAML with read-only Git/GitHub fixtures and isolated output paths.
	command := exec.CommandContext(t.Context(), "bash", "-c", readOnlyFixtures+preflight)
	command.Env = append(os.Environ(),
		"GITHUB_REF=refs/heads/master", "GITHUB_SHA="+strings.Repeat("a", 40),
		"GITHUB_REPOSITORY=example/fixture", "RUNNER_TEMP="+directory,
		"GITHUB_OUTPUT="+filepath.Join(directory, "output"), "FIXTURE_MOD="+manifest,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("release preflight failed: %v\n%s", err, output)
	}
	path := strings.ReplaceAll(versionFile, "${{ runner.temp }}", directory)
	if path != filepath.Join(directory, "go.mod") {
		t.Fatalf("setup-go will not recognize module manifest %q", versionFile)
	}
	//nolint:gosec // Read the manifest path produced by the repository-owned workflow in the fixture.
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != manifest {
		t.Fatalf("setup-go manifest = %q, error %v", actual, err)
	}
}

func TestReleasePreparationRejectsUnreproducibleManifestTree(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../.github/workflows/release.yml")
	if err != nil {
		t.Fatal(err)
	}
	_, preparation, _ := strings.Cut(string(contents), "        id: prepare\n")
	_, preparation, _ = strings.Cut(preparation, "        run: |\n")
	preparation, _, _ = strings.Cut(preparation, "\n  build:")
	preparation = strings.TrimPrefix(strings.ReplaceAll(preparation, "\n          ", "\n"), "          ")
	directory := t.TempDir()
	for _, script := range []string{"set-release-module-version", "check-release-module-version"} {
		path := filepath.Join("release-parent", "scripts", script)
		write(t, directory, path, "#!/usr/bin/env bash\nexit 0\n")
		run(t, directory, "chmod", "700", path)
	}
	const gitProof = `
git() {
  case "$*" in
    "worktree add --detach $RUNNER_TEMP/release-parent $PREPARED_PARENT") return 0 ;;
    "-C $RUNNER_TEMP/release-parent diff --exit-code $GITHUB_SHA --")
      printf 'compared entire tree\n' >> "$PROOF_TRACE"
      return "$PROOF_EXIT" ;;
    *) printf 'unexpected git call\n' >> "$PROOF_TRACE"; return 91 ;;
  esac
}
gh() { printf 'unexpected remote call\n' >> "$PROOF_TRACE"; return 92; }
`
	for _, test := range []struct{ exit, diagnostic string }{
		{"1", "does not match manifests generated from tested parent"},
		// A matching proof reaches the deliberately absent next generator; no release runs.
		{"0", "scripts/set-release-module-version: No such file or directory"},
	} {
		trace := filepath.Join(directory, "proof-trace-"+test.exit)
		//nolint:gosec // Execute the repository-owned preparation block with local proof fixtures only.
		command := exec.CommandContext(t.Context(), "bash", "-c", gitProof+preparation)
		command.Dir = directory
		command.Env = append(os.Environ(),
			"PREPARED_PARENT=fixture-parent", "PREPARED_VERSION=0.2.0", "RELEASE_VERSION=0.2.0",
			"GITHUB_SHA="+strings.Repeat("b", 40), "RUNNER_TEMP="+directory, "PROOF_TRACE="+trace,
			"PROOF_EXIT="+test.exit, "LC_ALL=C",
		)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), test.diagnostic) {
			t.Fatalf("release proof exit %s: %v\n%s", test.exit, err, output)
		}
		if calls := run(t, directory, "cat", trace); calls != "compared entire tree\n" {
			t.Fatalf("release preparation proof calls = %q", calls)
		}
	}
}
