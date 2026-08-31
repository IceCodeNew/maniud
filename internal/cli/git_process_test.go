package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/compose"
)

//nolint:paralleltest // This test owns PATH and hostile Git environment variables.
func TestRunGitUsesMinimalEnvironment(t *testing.T) {
	gitPath := installFakeGit(t)
	for _, name := range []string{
		"GIT_CONFIG_COUNT", "GIT_DIR", "GIT_EXEC_PATH", "GIT_OBJECT_DIRECTORY",
		"GIT_SSH_COMMAND", "GIT_WORK_TREE", "HOME", "SSH_ASKPASS",
	} {
		t.Setenv(name, "hostile")
	}
	writeFakeGit(t, gitPath, `
test "${GIT_CONFIG_COUNT+set}" != set
test "${GIT_DIR+set}" != set
test "${GIT_EXEC_PATH+set}" != set
test "${GIT_OBJECT_DIRECTORY+set}" != set
test "${GIT_SSH_COMMAND+set}" != set
test "${GIT_WORK_TREE+set}" != set
test "${HOME+set}" != set
test "${SSH_ASKPASS+set}" != set
test "$GIT_CONFIG_GLOBAL" = /dev/null
test "$GIT_CONFIG_NOSYSTEM" = 1
test "$GIT_OPTIONAL_LOCKS" = 0
test "$GIT_TERMINAL_PROMPT" = 0
test "$LANG" = C
test "$LC_ALL" = C
printf 'safe\n'
`)

	output, err := runGit(t.Context(), t.TempDir(), "status")
	if err != nil || string(output) != "safe\n" {
		t.Fatalf("runGit() = %q, %v", output, err)
	}
}

//nolint:paralleltest // This test owns PATH and the user Git environment allowlist.
func TestRunGitWithUserConfigPreservesSigningEnvironment(t *testing.T) {
	gitPath := installFakeGit(t)
	home := t.TempDir()
	for name, value := range map[string]string{
		"HOME": home, xdgConfigHomeEnvironment: home + "/config", "XDG_RUNTIME_DIR": home + "/runtime",
		"GNUPGHOME": home + "/gnupg", "GPG_TTY": "/dev/ttys001", "SSH_AUTH_SOCK": home + "/agent.sock",
		"GIT_DIR": "hostile", "GIT_CONFIG_GLOBAL": "hostile",
	} {
		t.Setenv(name, value)
	}
	writeFakeGit(t, gitPath, `
test "$HOME" = '`+home+`'
test "$XDG_CONFIG_HOME" = '`+home+`/config'
test "$XDG_RUNTIME_DIR" = '`+home+`/runtime'
test "$GNUPGHOME" = '`+home+`/gnupg'
test "$GPG_TTY" = /dev/ttys001
test "$SSH_AUTH_SOCK" = '`+home+`/agent.sock'
test "${GIT_DIR+set}" != set
test "${GIT_CONFIG_GLOBAL+set}" != set
test "${GIT_CONFIG_NOSYSTEM+set}" != set
test "$GIT_TERMINAL_PROMPT" = 0
printf 'signed\n'
`)

	output, err := runGitWithUserConfig(t.Context(), t.TempDir(), "commit", "-S")
	if err != nil || string(output) != "signed\n" {
		t.Fatalf("runGitWithUserConfig() = %q, %v", output, err)
	}
}

//nolint:paralleltest // This test owns PATH while exercising process failures.
func TestRunGitRejectsTimeoutAndOversizedOutput(t *testing.T) {
	gitPath := installFakeGit(t)
	writeFakeGit(t, gitPath, `exec sleep 5`)
	deadline, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := runGit(deadline, t.TempDir(), "status"); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("runGit(timeout) error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("runGit(timeout) took %s", elapsed)
	}

	large := filepath.Join(t.TempDir(), "large")
	if err := os.WriteFile(large, make([]byte, maximumGitOutputBytes+1), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFakeGit(t, gitPath, `cat `+large)
	output, err := runGit(t.Context(), t.TempDir(), "status")
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("runGit(oversized output) = %d bytes, %v", len(output), err)
	}
}

func TestGitSourceRejectsHostileRepositoryConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte("services: {}\n"), 0o600)
	commitApplyTestRepository(t, root, "compose.yaml")
	if _, err := runGit(
		t.Context(), root, "config", "--local", "filter.maniud.process", "false",
	); err != nil {
		t.Fatalf("git config error = %v", err)
	}

	_, err := loadTrackedComposeSource(t.Context(), "compose.yaml", root, nil, t.TempDir())
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("loadTrackedComposeSource(hostile config) error = %v", err)
	}
}

func TestValidateGitProcessConfigurationRejectsExecutableConfig(t *testing.T) {
	t.Parallel()

	for _, key := range []string{
		"include.path",
		"includeIf.onbranch:main.path",
		"filter.maniud.process",
		"gpg.program",
		"gpg.format",
		"gpg.openpgp.program",
		"gpg.ssh.program",
		"gpg.ssh.defaultKeyCommand",
		"gpg.x509.program",
		"url.ext::command.insteadOf",
		"url.ext::command.pushInsteadOf",
	} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			if _, err := runGit(t.Context(), root, "init", "--quiet"); err != nil {
				t.Fatalf("git init error = %v", err)
			}
			if _, err := runGit(t.Context(), root, "config", "--local", key, "hostile"); err != nil {
				t.Fatalf("git config error = %v", err)
			}
			if err := validateGitProcessConfiguration(t.Context(), root); !errors.Is(err, compose.ErrInvalidSource) {
				t.Fatalf("validateGitProcessConfiguration(%q) error = %v", key, err)
			}
		})
	}
}

func TestValidateGitProcessConfigurationAllowsInertLocalConfig(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := runGit(t.Context(), root, "init", "--quiet"); err != nil {
		t.Fatalf("git init error = %v", err)
	}
	for key, value := range map[string]string{
		"alias.status":      "!false",
		"core.fsmonitor":    "false",
		"core.hooksPath":    t.TempDir(),
		"core.pager":        "false",
		"credential.helper": "!false",
	} {
		if _, err := runGit(t.Context(), root, "config", "--local", key, value); err != nil {
			t.Fatalf("git config %s error = %v", key, err)
		}
	}
	if err := validateGitProcessConfiguration(t.Context(), root); err != nil {
		t.Fatalf("validateGitProcessConfiguration() error = %v", err)
	}
}

func TestSplitNullTerminated(t *testing.T) {
	t.Parallel()

	fields, valid := splitNullTerminated([]byte("local\x00core.filemode\x00"))
	if !valid || len(fields) != 2 || string(fields[1]) != "core.filemode" {
		t.Fatalf("splitNullTerminated(valid) = %q, %t", fields, valid)
	}
	for _, value := range [][]byte{nil, []byte("unterminated")} {
		fields, valid = splitNullTerminated(value)
		if valid != (value == nil) || fields != nil {
			t.Fatalf("splitNullTerminated(%q) = %q, %t", value, fields, valid)
		}
	}

	if !hostileGitConfigKey("FILTER.driver.required") || hostileGitConfigKey("alias.status") {
		t.Fatal("hostileGitConfigKey() classification mismatch")
	}
}

func TestValidGitRemoteURL(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"/srv/git/maniud.git",
		"https://example.invalid/maniud.git",
		"file:///srv/git/maniud.git",
		"file://localhost/srv/git/maniud.git",
	} {
		if !validGitRemoteURL(value) {
			t.Fatalf("validGitRemoteURL(%q) = false", value)
		}
	}
	for _, value := range []string{
		"", "relative.git", "/srv/../git", "http://example.invalid/repo",
		"ssh://example.invalid/repo", "ext::command", "https:///repo",
		"https://example.invalid/repo#fragment", "file://remote/repo", "file:///repo?query",
		"https://example.invalid/repo\nother", "https://example.invalid/%zz",
	} {
		if validGitRemoteURL(value) {
			t.Fatalf("validGitRemoteURL(%q) = true", value)
		}
	}
}
