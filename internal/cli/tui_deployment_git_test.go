package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
)

func TestDeploymentGitConfigurationFreezesBuiltInTransforms(t *testing.T) {
	t.Parallel()

	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	for _, setting := range [][2]string{
		{"core.autocrlf", "input"},
		{"core.eol", "lf"},
		{"core.safecrlf", "warn"},
		{"core.checkRoundtripEncoding", "SHIFT-JIS"},
		{"user.name", "ignored"},
	} {
		if _, err := runGit(
			t.Context(), repository, "config", "--local", setting[0], setting[1],
		); err != nil {
			t.Fatalf("git config %s error = %v", setting[0], err)
		}
	}
	arguments, err := deploymentGitConfiguration(t.Context(), repository)
	if err != nil {
		t.Fatalf("deploymentGitConfiguration() error = %v", err)
	}
	want := []string{
		"-c", "core.autocrlf=input",
		"-c", "core.eol=lf",
		"-c", "core.safecrlf=warn",
		"-c", "core.checkroundtripencoding=SHIFT-JIS",
	}
	if !slices.Equal(arguments, want) {
		t.Fatalf("deploymentGitConfiguration() = %#v, want %#v", arguments, want)
	}
	for _, key := range []string{
		"core.autocrlf", "CORE.EOL", "core.safecrlf", "core.checkroundtripencoding",
	} {
		if !deploymentGitConfigurationKey(key) {
			t.Fatalf("deploymentGitConfigurationKey(%q) = false", key)
		}
	}
	if deploymentGitConfigurationKey("user.name") {
		t.Fatal("deploymentGitConfigurationKey(user.name) = true")
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = deploymentGitConfiguration(cancelled, repository); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("deploymentGitConfiguration(cancelled) error = %v", err)
	}
	if _, err = absoluteGitPath(t.Context(), t.TempDir(), "objects"); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("absoluteGitPath(non-repository) error = %v", err)
	}
}

//nolint:cyclop // The test covers the complete bounded optional-file contract.
func TestReadOptionalGitFileRejectsUnsafePaths(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	missing := filepath.Join(root, "missing")
	if content, identity, present, err := readOptionalGitFile(missing); err != nil ||
		present || content != nil || identity != nil {
		t.Fatalf("readOptionalGitFile(missing) = %q, %#v, %t, %v", content, identity, present, err)
	}
	path := filepath.Join(root, "attributes")
	if err := os.WriteFile(path, []byte("*.yaml text\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(attributes) error = %v", err)
	}
	if content, identity, present, err := readOptionalGitFile(path); err != nil || !present ||
		string(content) != "*.yaml text\n" || identity == nil {
		t.Fatalf("readOptionalGitFile(regular) = %q, %#v, %t, %v", content, identity, present, err)
	}
	if _, _, _, err := readOptionalGitFile(root); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("readOptionalGitFile(directory) error = %v", err)
	}
	oversized := filepath.Join(root, "oversized")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", maximumComposeSourceBytes+1)), 0o600); err != nil {
		t.Fatalf("WriteFile(oversized) error = %v", err)
	}
	if _, _, _, err := readOptionalGitFile(oversized); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("readOptionalGitFile(oversized) error = %v", err)
	}
}

//nolint:funlen // Each case isolates one otherwise uncontrollable filesystem proof boundary.
func TestReadOptionalGitFileRejectsChangedOrUnreadableFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "attributes")
	if err := os.WriteFile(path, []byte("*.yaml text\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(attributes) error = %v", err)
	}
	directoryInfo, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat(directory) error = %v", err)
	}
	tests := []struct {
		name      string
		configure func(*deploymentGitFileOperations)
	}{
		{"lstat error", func(operations *deploymentGitFileOperations) {
			operations.lstat = func(string) (os.FileInfo, error) { return nil, errDeploymentCoverage }
		}},
		{"open error", func(operations *deploymentGitFileOperations) {
			operations.open = func(string) (*os.File, error) { return nil, errDeploymentCoverage }
		}},
		{"injected read failure", func(operations *deploymentGitFileOperations) {
			operations.read = func(io.Reader) ([]byte, error) { return nil, errDeploymentCoverage }
		}},
		{"descriptor stat error", func(operations *deploymentGitFileOperations) {
			operations.stat = func(*os.File) (os.FileInfo, error) { return nil, errDeploymentCoverage }
		}},
		{"close error", func(operations *deploymentGitFileOperations) {
			closeFile := operations.close
			operations.close = func(file *os.File) error {
				return errors.Join(closeFile(file), errDeploymentCoverage)
			}
		}},
		{"visible stat error", func(operations *deploymentGitFileOperations) {
			lstat := operations.lstat
			calls := 0
			operations.lstat = func(path string) (os.FileInfo, error) {
				calls++
				if calls == 2 {
					return nil, errDeploymentCoverage
				}

				return lstat(path)
			}
		}},
		{"grew while reading", func(operations *deploymentGitFileOperations) {
			operations.read = func(io.Reader) ([]byte, error) {
				return make([]byte, maximumComposeSourceBytes+1), nil
			}
		}},
		{"descriptor is not regular", func(operations *deploymentGitFileOperations) {
			operations.stat = func(*os.File) (os.FileInfo, error) { return directoryInfo, nil }
		}},
		{"descriptor changed", func(operations *deploymentGitFileOperations) {
			operations.sameFile = func(os.FileInfo, os.FileInfo) bool { return false }
		}},
		{"path changed", func(operations *deploymentGitFileOperations) {
			calls := 0
			operations.sameFile = func(os.FileInfo, os.FileInfo) bool {
				calls++

				return calls == 1
			}
		}},
	}
	for _, test := range tests {
		operations := defaultDeploymentGitFileOperations()
		test.configure(&operations)
		if _, _, _, err := readOptionalGitFileWithOperations(path, operations); !errors.Is(
			err, errDeploymentEditInvalid,
		) {
			t.Fatalf("%s error = %v", test.name, err)
		}
	}
}

func TestDeploymentGitIsolationContainsFilesystemFailures(t *testing.T) {
	t.Parallel()

	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	transform := deploymentGitTransform{
		infoAttributes: []byte("*.yaml text\n"), infoPresent: true, objectFormat: "sha1",
	}
	for _, test := range []struct {
		name      string
		configure func(*deploymentGitFileOperations)
	}{
		{"temporary directory", func(operations *deploymentGitFileOperations) {
			operations.mkdirTemp = func(string, string) (string, error) {
				return "", errDeploymentCoverage
			}
		}},
		{"attributes write", func(operations *deploymentGitFileOperations) {
			operations.writeFile = func(string, []byte, os.FileMode) error {
				return errDeploymentCoverage
			}
		}},
		{"object directory", func(operations *deploymentGitFileOperations) {
			operations.mkdir = func(string, os.FileMode) error { return errDeploymentCoverage }
		}},
	} {
		operations := defaultDeploymentGitFileOperations()
		test.configure(&operations)
		isolation, err := newDeploymentGitIsolationWithOperations(
			t.Context(), repository, transform, false, "", operations,
		)
		if err == nil || isolation.root != "" {
			t.Fatalf("%s isolation = %#v, %v", test.name, isolation, err)
		}
	}
}

//nolint:cyclop // Both index modes share one isolation lifecycle proof.
func TestDeploymentGitIsolationUsesFrozenAttributesAndSelectedIndex(t *testing.T) {
	t.Parallel()

	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	transform := deploymentGitTransform{
		infoAttributes: []byte(deploymentComposeEntry + " text eol=lf\n"),
		infoPresent:    true,
		objectFormat:   "sha1",
		configArguments: []string{
			"-c", "core.autocrlf=input",
		},
	}
	for _, useRepositoryIndex := range []bool{false, true} {
		isolation, err := newDeploymentGitIsolation(
			t.Context(), repository, transform, useRepositoryIndex,
		)
		if err != nil {
			t.Fatalf("newDeploymentGitIsolation(%t) error = %v", useRepositoryIndex, err)
		}
		if _, err = isolation.Run(t.Context(), nil, "rev-parse", "--git-dir"); err != nil {
			t.Fatalf("isolation.Run(%t) error = %v", useRepositoryIndex, err)
		}
		root := isolation.root
		if err = isolation.Close(); err != nil {
			t.Fatalf("isolation.Close(%t) error = %v", useRepositoryIndex, err)
		}
		if _, err = os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("isolation root remains after Close: %v", err)
		}
	}
	if err := (deploymentGitIsolation{}).Close(); err != nil {
		t.Fatalf("empty isolation Close() error = %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if isolation, err := newDeploymentGitIsolation(cancelled, repository, transform, false); err == nil ||
		isolation.root != "" {
		t.Fatalf("newDeploymentGitIsolation(cancelled) = %#v, %v", isolation, err)
	}
	if isolation, err := newDeploymentGitIsolation(t.Context(), t.TempDir(), transform, false); err == nil ||
		isolation.root != "" {
		t.Fatalf("newDeploymentGitIsolation(non-repository) = %#v, %v", isolation, err)
	}
}

func TestDeploymentIndexFileOperationsReportFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	indexPath := filepath.Join(root, "index")
	if err := os.WriteFile(indexPath, []byte("index"), 0o600); err != nil {
		t.Fatalf("WriteFile(index) error = %v", err)
	}

	operations := defaultDeploymentGitFileOperations()
	operations.open = func(string) (*os.File, error) { return nil, errDeploymentCoverage }
	if _, err := createDeploymentIndexLockWithOperations(indexPath, operations); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("createDeploymentIndexLockWithOperations(open failure) error = %v", err)
	}

	operations = defaultDeploymentGitFileOperations()
	operations.stat = func(*os.File) (os.FileInfo, error) { return nil, errDeploymentCoverage }
	remove := operations.remove
	operations.remove = func(path string) error {
		return errors.Join(remove(path), errDeploymentCoverage)
	}
	if _, err := createDeploymentIndexLockWithOperations(indexPath, operations); !errors.Is(
		err, errDeploymentCoverage,
	) || !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("createDeploymentIndexLockWithOperations(proof failure) error = %v", err)
	}
	operations = defaultDeploymentGitFileOperations()
	operations.copy = func(io.Writer, io.Reader) (int64, error) { return 0, errDeploymentCoverage }
	if _, err := createDeploymentIndexLockWithOperations(indexPath, operations); !errors.Is(
		err, errDeploymentCoverage,
	) {
		t.Fatalf("createDeploymentIndexLockWithOperations(copy failure) error = %v", err)
	}

	operations = defaultDeploymentGitFileOperations()
	operations.open = func(string) (*os.File, error) { return nil, errDeploymentCoverage }
	if _, err := deploymentIndexDigestWithOperations(indexPath, operations); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("deploymentIndexDigestWithOperations(open failure) error = %v", err)
	}

	operations = defaultDeploymentGitFileOperations()
	operations.copy = func(io.Writer, io.Reader) (int64, error) { return 0, errDeploymentCoverage }
	if _, err := deploymentIndexDigestWithOperations(indexPath, operations); !errors.Is(
		err, errDeploymentCoverage,
	) {
		t.Fatalf("deploymentIndexDigestWithOperations(copy failure) error = %v", err)
	}

	if _, err := deploymentIndexTree(t.Context(), root, filepath.Join(root, "missing")); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("deploymentIndexTree(invalid index) error = %v", err)
	}
}

//nolint:paralleltest // The test installs a process-wide PATH wrapper around Git.
func TestDeploymentGitIsolationRejectsLostRepositoryIndex(t *testing.T) {
	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	transform, err := captureDeploymentGitTransform(t.Context(), repository)
	if err != nil {
		t.Fatalf("captureDeploymentGitTransform() error = %v", err)
	}
	installDeploymentGitWrapper(t, ""+
		"previous=\n"+
		"for argument do\n"+
		"  if [ \"$previous\" = --git-path ] && [ \"$argument\" = index ]; then exit 1; fi\n"+
		"  previous=$argument\n"+
		"done\n")
	if isolation, err := newDeploymentGitIsolation(
		t.Context(), repository, transform, true,
	); err == nil || isolation.root != "" {
		t.Fatalf("newDeploymentGitIsolation(lost index) = %#v, %v", isolation, err)
	}
}

//nolint:paralleltest // Each subtest installs a process-wide PATH wrapper around Git.
func TestPrepareDeploymentConfirmationRejectsGitProofFailures(t *testing.T) {
	for _, argument := range []string{
		initCommand, "check-attr", "read-tree", "hash-object", "cat-file", "ls-tree", "update-index", "write-tree", "diff",
	} {
		t.Run(argument, func(t *testing.T) {
			draft := newDeploymentDraftFixture(t)
			installDeploymentGitWrapper(t, ""+
				"for argument do\n"+
				"  if [ \"$argument\" = "+shellArgument(argument)+" ]; then exit 1; fi\n"+
				"done\n")
			if _, _, err := prepareDeploymentConfirmation(t.Context(), draft); err == nil {
				t.Fatalf("prepareDeploymentConfirmation(%s) succeeded", argument)
			}
		})
	}

	t.Run("invalid candidate", func(t *testing.T) {
		draft := newDeploymentDraftFixture(t)
		draft.candidate.Content = []byte("invalid compose")
		if _, _, err := prepareDeploymentConfirmation(t.Context(), draft); err == nil {
			t.Fatal("prepareDeploymentConfirmation(invalid candidate) succeeded")
		}
	})

	t.Run("cancelled capture", func(t *testing.T) {
		draft := newDeploymentDraftFixture(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, _, err := prepareDeploymentConfirmation(ctx, draft); err == nil {
			t.Fatal("prepareDeploymentConfirmation(cancelled) succeeded")
		}
	})
}

//nolint:paralleltest // The test installs a process-wide PATH wrapper around Git.
func TestDeploymentGitConfigurationRejectsMalformedOutput(t *testing.T) {
	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	installDeploymentGitWrapper(t, ""+
		"for argument do\n"+
		"  if [ \"$argument\" = --list ]; then printf unterminated; exit 0; fi\n"+
		"done\n")
	if _, err := deploymentGitConfiguration(t.Context(), repository); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("deploymentGitConfiguration(malformed) error = %v", err)
	}
}

//nolint:paralleltest // The test installs a process-wide PATH wrapper around Git.
func TestExactStagedStatusRejectsMalformedOutput(t *testing.T) {
	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	installDeploymentGitWrapper(t, ""+
		"for argument do\n"+
		"  if [ \"$argument\" = status ]; then printf unterminated; exit 0; fi\n"+
		"done\n")
	if exactStagedPathStatusWithAttributes(
		t.Context(), repository, []string{deploymentComposeEntry}, "M", "",
	) {
		t.Fatal("exactStagedPathStatusWithAttributes(malformed status) = true")
	}
}

func TestCaptureDeploymentGitTransformContainsEachBoundary(t *testing.T) {
	t.Parallel()

	if _, err := captureDeploymentGitTransform(t.Context(), t.TempDir()); err == nil {
		t.Fatal("captureDeploymentGitTransform(non-repository) succeeded")
	}
	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	path, err := absoluteGitPath(t.Context(), repository, "info/attributes")
	if err != nil {
		t.Fatalf("absoluteGitPath(info/attributes) error = %v", err)
	}
	if err = os.Mkdir(path, 0o700); err != nil {
		t.Fatalf("Mkdir(info/attributes) error = %v", err)
	}
	if _, err = captureDeploymentGitTransform(t.Context(), repository); err == nil {
		t.Fatal("captureDeploymentGitTransform(attributes directory) succeeded")
	}
}

//nolint:cyclop // The test exercises the complete SHA-256 preview, stage, and rollback lifecycle.
func TestTUIDeploymentWorkspaceSupportsSHA256Repository(t *testing.T) {
	t.Parallel()

	repository := t.TempDir()
	if _, err := runGit(t.Context(), repository, "init", "--quiet", "--object-format=sha256"); err != nil {
		t.Fatalf("git init sha256 error = %v", err)
	}
	path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	content := deploymentComposeFixture()
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := runGit(t.Context(), repository, "add", "--", deploymentComposeEntry); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	commitDeploymentIndex(t, repository, "Add SHA-256 deployment")
	runtimeBase := t.TempDir()
	environment := map[string]string{testComposeDisableEnvFile: trueValue}
	source, err := loadTrackedComposeSource(t.Context(), path, repository, environment, runtimeBase)
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
	workspace := &tuiDeploymentWorkspace{environment: environment, runtimeBase: runtimeBase}
	request := application.Request{Source: source, Service: applyServiceValue}
	preview, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
	)
	if err != nil || preview.Diff == "" {
		t.Fatalf("Preview() = %#v, %v", preview, err)
	}
	staged, err := workspace.Stage(t.Context())
	if err != nil || staged.Diff != preview.Diff {
		t.Fatalf("Stage() = %#v, %v", staged, err)
	}
	if err = workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard() error = %v", err)
	}
	assertTUIDeploymentContent(t, repository, deploymentComposeEntry, content)
}

//nolint:paralleltest // The test installs a process-wide PATH wrapper around Git.
func TestCaptureAndStageDeploymentGitTransformContainGitFailures(t *testing.T) {
	t.Run("capture configuration", func(t *testing.T) {
		_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
		installDeploymentGitWrapper(t, ""+
			"for argument do\n"+
			"  if [ \"$argument\" = config ]; then exit 1; fi\n"+
			"done\n")
		if _, err := captureDeploymentGitTransform(t.Context(), repository); err == nil {
			t.Fatal("captureDeploymentGitTransform(config failure) succeeded")
		}
	})

	for _, argument := range []string{initCommand, "index", "check-attr"} {
		t.Run("stage "+argument, func(t *testing.T) {
			draft := newDeploymentDraftFixture(t)
			installDeploymentGitWrapper(t, ""+
				"for argument do\n"+
				"  if [ \"$argument\" = "+shellArgument(argument)+" ]; then exit 1; fi\n"+
				"done\n")
			if err := stageConfirmedDeploymentWith(
				t.Context(), draft,
				func(string, string, []byte, []byte) (bool, error) {
					t.Fatal("replacement ran before the Git proof completed")

					return false, nil
				},
				defaultDeploymentGitFileOperations(),
			); err == nil {
				t.Fatalf("stageConfirmedDeploymentWith(%s) succeeded", argument)
			}
		})
	}

	t.Run("stage add", func(t *testing.T) {
		draft := newDeploymentDraftFixture(t)
		installDeploymentGitWrapper(t, ""+
			"for argument do\n"+
			"  if [ \"$argument\" = add ]; then exit 1; fi\n"+
			"done\n")
		if err := stageConfirmedDeploymentWith(
			t.Context(), draft, replaceDeploymentEntry, defaultDeploymentGitFileOperations(),
		); !errors.Is(err, errDeploymentPublishFailed) {
			t.Fatalf("stageConfirmedDeploymentWith(add) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
		assertDeploymentIndexUnlocked(t, draft.repository)
	})
}

//nolint:paralleltest // The test installs a process-wide PATH wrapper around Git.
func TestStageConfirmedDeploymentReportsUnknownWorktreeWhenRestoreFails(t *testing.T) {
	draft := newDeploymentDraftFixture(t)
	installDeploymentGitWrapper(t, ""+
		"for argument do\n"+
		"  if [ \"$argument\" = add ]; then exit 1; fi\n"+
		"done\n")
	calls := 0
	err := stageConfirmedDeploymentWith(
		t.Context(), draft,
		func(string, string, []byte, []byte) (bool, error) {
			calls++
			if calls == 1 {
				return true, nil
			}

			return false, errDeploymentCoverage
		},
		defaultDeploymentGitFileOperations(),
	)
	if !errors.Is(err, errDeploymentWorktreeUnknown) || calls != 2 {
		t.Fatalf("stageConfirmedDeploymentWith(restore failure) = %v, calls = %d", err, calls)
	}
}

func TestStageConfirmedDeploymentRejectsConcurrentIndex(t *testing.T) {
	t.Parallel()

	draft := newDeploymentDraftFixture(t)
	concurrentPath := filepath.Join(draft.repository, "concurrent")
	if err := os.WriteFile(concurrentPath, []byte("concurrent\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(concurrent) error = %v", err)
	}
	if _, err := runGit(t.Context(), draft.repository, "add", "--", "concurrent"); err != nil {
		t.Fatalf("git add concurrent error = %v", err)
	}
	if err := stageConfirmedDeploymentWith(
		t.Context(), draft,
		func(string, string, []byte, []byte) (bool, error) {
			t.Fatal("replacement ran after concurrent index mutation")

			return false, nil
		},
		defaultDeploymentGitFileOperations(),
	); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("stageConfirmedDeploymentWith(concurrent index) error = %v", err)
	}
	assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
	assertDeploymentIndexUnlocked(t, draft.repository)
}

func TestStageConfirmedDeploymentReportsIndexPublicationFailures(t *testing.T) {
	t.Parallel()

	t.Run("digest changed", func(t *testing.T) {
		t.Parallel()

		draft := newDeploymentDraftFixture(t)
		operations := defaultDeploymentGitFileOperations()
		copyFile := operations.copy
		calls := 0
		operations.copy = func(destination io.Writer, source io.Reader) (int64, error) {
			calls++
			if calls == 2 {
				return 0, errDeploymentCoverage
			}

			return copyFile(destination, source)
		}
		if err := stageConfirmedDeploymentWith(
			t.Context(), draft, replaceDeploymentEntry, operations,
		); !errors.Is(err, errDeploymentCoverage) {
			t.Fatalf("stageConfirmedDeploymentWith(digest failure) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
		assertDeploymentIndexUnlocked(t, draft.repository)
	})

	t.Run("rename and cleanup", func(t *testing.T) {
		t.Parallel()

		draft := newDeploymentDraftFixture(t)
		operations := defaultDeploymentGitFileOperations()
		operations.rename = func(string, string) error { return errDeploymentCoverage }
		remove := operations.remove
		operations.remove = func(path string) error {
			return errors.Join(remove(path), errDeploymentCoverage)
		}
		if err := stageConfirmedDeploymentWith(
			t.Context(), draft, replaceDeploymentEntry, operations,
		); !errors.Is(err, errDeploymentCoverage) {
			t.Fatalf("stageConfirmedDeploymentWith(rename failure) error = %v", err)
		}
		assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
		assertDeploymentIndexUnlocked(t, draft.repository)
	})
}

func TestStageConfirmedDeploymentRollsBackWhenIsolationCleanupFails(t *testing.T) {
	t.Parallel()

	draft := newDeploymentDraftFixture(t)
	operations := defaultDeploymentGitFileOperations()
	removeAll := operations.removeAll
	operations.removeAll = func(path string) error {
		return errors.Join(removeAll(path), errDeploymentCoverage)
	}
	err := stageConfirmedDeploymentWith(
		t.Context(), draft, replaceDeploymentEntry, operations,
	)
	if !errors.Is(err, errDeploymentCoverage) {
		t.Fatalf("stageConfirmedDeploymentWith(cleanup failure) error = %v", err)
	}
	assertTUIDeploymentContent(t, draft.repository, draft.entry, draft.source.Content)
	if _, err = cleanGitTree(t.Context(), draft.repository); err != nil {
		t.Fatalf("cleanup failure rescue left repository dirty: %v", err)
	}
}

//nolint:paralleltest // The test installs a process-wide PATH wrapper around Git.
func TestCaptureDeploymentGitTransformRejectsMalformedObjectFormat(t *testing.T) {
	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	installDeploymentGitWrapper(t, ""+
		"for argument do\n"+
		"  if [ \"$argument\" = --show-object-format ]; then printf sha512; exit 0; fi\n"+
		"done\n")
	if _, err := captureDeploymentGitTransform(t.Context(), repository); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("captureDeploymentGitTransform(malformed object format) error = %v", err)
	}
}
