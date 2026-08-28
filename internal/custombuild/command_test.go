package custombuild

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestBuildEnvironmentPreservesGoConfiguration(t *testing.T) {
	goEnvironment := filepath.Join(t.TempDir(), "go-env")
	t.Setenv("GOENV", goEnvironment)
	t.Setenv("GOPROXY", "https://proxy.example.test")
	t.Setenv("GOOS", "old")

	got := buildEnvironment("linux", "arm64")
	for _, want := range []string{
		"GOENV=" + goEnvironment,
		"GOPROXY=https://proxy.example.test",
		"GOOS=linux",
		"GOARCH=arm64",
		"CGO_ENABLED=0",
		"GOFLAGS=",
		"GOWORK=off",
	} {
		if !slices.Contains(got, want) {
			t.Fatalf("buildEnvironment() = %q; missing %q", got, want)
		}
	}
	if slices.Contains(got, "GOOS=old") {
		t.Fatalf("buildEnvironment() = %q; kept old GOOS", got)
	}
}

func TestRunCommandBoundsAndReportsProcessFailures(t *testing.T) {
	t.Parallel()

	output, err := runCommand(t.Context(), t.TempDir(), nil, "print", "sh", "-c", "printf "+testSuccess)
	if err != nil || string(output) != testSuccess {
		t.Fatalf("runCommand(success) = %q, %v", output, err)
	}
	if _, err = runCommand(t.Context(), t.TempDir(), nil, "empty", "sh", "-c", "exit 3"); err == nil ||
		!strings.Contains(err.Error(), "empty") {
		t.Fatalf("runCommand(empty failure) error = %v", err)
	}
	if _, err = runCommand(
		t.Context(), t.TempDir(), nil, "short", "sh", "-c", "printf failure >&2; exit 3",
	); err == nil || !strings.Contains(err.Error(), "failure") {
		t.Fatalf("runCommand(short failure) error = %v", err)
	}
	large := strings.Repeat("x", maximumCommandError+1)
	if _, err = runCommand(
		t.Context(), t.TempDir(), nil, "long", "sh", "-c",
		`printf %s "$1" >&2; exit 3`, "sh", large,
	); err == nil || len(err.Error()) > maximumCommandError+100 {
		t.Fatalf("runCommand(long failure) error length = %d", len(err.Error()))
	}
}

func TestVerifyBuildMetadataRejectsIncompleteEvidence(t *testing.T) {
	t.Parallel()

	valid := "maniud: " + testGoVersion + "\n\tdep\t" + projectModule + "\tv0.0.0\n"
	if err := verifyBuildMetadata(valid, testGoVersion); err != nil {
		t.Fatalf("verifyBuildMetadata(valid) error = %v", err)
	}
	for _, value := range []string{
		"maniud: go1.27.0",
		"maniud: go1.26.0\n\tdep\t" + projectModule + "\tv0.0.0\n",
		"maniud: go1.27.0\n",
	} {
		if err := verifyBuildMetadata(value, testGoVersion); !errors.Is(err, errDependencyMismatch) {
			t.Fatalf("verifyBuildMetadata(%q) error = %v", value, err)
		}
	}
}

func TestPathWithinHandlesRootNestedSiblingAndResolutionFailure(t *testing.T) {
	t.Parallel()

	parent := filepath.Join(string(filepath.Separator), "source")
	if !pathWithin(parent, parent) || !pathWithin(parent, filepath.Join(parent, "nested")) {
		t.Fatal("pathWithin() rejected the source root or a nested path")
	}
	if pathWithin(parent, filepath.Join(string(filepath.Separator), "sibling")) {
		t.Fatal("pathWithin() accepted a sibling path")
	}
	if pathWithinWith(parent, parent, func(string, string) (string, error) {
		return "", errCustomBuildTest
	}) {
		t.Fatal("pathWithinWith() accepted a failed relative path")
	}
}

func TestRemoveIfExistsHandlesMissingFileAndFilesystemFailure(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if err := removeIfExists(missing); err != nil {
		t.Fatalf("removeIfExists(missing) error = %v", err)
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, []byte("content"), generatedFileMode); err != nil {
		t.Fatal(err)
	}
	if err := removeIfExists(file); err != nil {
		t.Fatalf("removeIfExists(file) error = %v", err)
	}
	nonempty := filepath.Join(root, "nonempty")
	if err := os.Mkdir(nonempty, outputDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonempty, "file"), nil, generatedFileMode); err != nil {
		t.Fatal(err)
	}
	if err := removeIfExists(nonempty); err == nil {
		t.Fatal("removeIfExists(nonempty directory) unexpectedly succeeded")
	}
}
