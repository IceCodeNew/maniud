//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/llm"
	"github.com/IceCodeNew/maniud/internal/tui"
)

var errLLMConfigFixture = errors.New("LLM configuration fixture failure")

func TestLLMConfigurationResolvesDotenvPrecedenceAndExplicitSecretUnset(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	working := t.TempDir()
	xdg := filepath.Join(home, "config")
	writeTestLLMEnv(t, filepath.Join(working, llmConfigName), 0o600, strings.Join([]string{
		"PROVIDER=openai", "MANIUD_LLM_PROVIDER=${PROVIDER}",
		"MANIUD_LLM_MODEL=first", "MANIUD_LLM_MODEL=second",
		"MANIUD_LLM_TIMEOUT=45", "OPENAI_API_KEY=current-secret", "",
	}, "\n"))
	writeTestLLMEnv(t, filepath.Join(xdg, llmConfigDirectory, llmConfigName), 0o600, strings.Join([]string{
		"MANIUD_LLM_PROVIDER=deepseek", "MANIUD_LLM_MODEL=lower-model",
		"MANIUD_LLM_TIMEOUT=60", "OPENAI_API_KEY=xdg-secret", "",
	}, "\n"))
	resolved, err := loadLLMConfiguration(t.Context(), map[string]string{
		homeEnvironment: home, xdgConfigHomeEnvironment: xdg,
		llmProviderEnvironment: "", openAIKeyEnvironment: "",
	}, working)
	if err != nil {
		t.Fatalf("loadLLMConfiguration() error = %v", err)
	}
	if resolved.config.Provider != llm.ProviderOpenAI || resolved.config.Model != "second" ||
		resolved.config.Timeout.Seconds() != 45 || resolved.config.APIKey != "" || resolved.identity != "" ||
		resolved.keySource != "explicitly unset in process environment" {
		t.Fatalf("resolved = %#v", resolved)
	}
}

func TestLLMConfigurationSkipsMalformedAndUnsafeSources(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		content string
		mode    os.FileMode
	}{
		{name: "malformed", content: "MANIUD_LLM_PROVIDER='openai\n", mode: 0o600},
		{name: "secret permissions", content: "OPENAI_API_KEY=unsafe\n", mode: 0o644},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			working := t.TempDir()
			xdg := filepath.Join(home, "config")
			writeTestLLMEnv(t, filepath.Join(working, llmConfigName), test.mode, test.content)
			writeTestLLMEnv(t, filepath.Join(xdg, llmConfigDirectory, llmConfigName), 0o600,
				validTestLLMEnv("xdg-secret"))
			resolved, err := loadLLMConfiguration(t.Context(), map[string]string{
				homeEnvironment: home, xdgConfigHomeEnvironment: xdg,
			}, working)
			if err != nil {
				t.Fatalf("loadLLMConfiguration() error = %v", err)
			}
			if resolved.config.APIKey != "xdg-secret" || len(resolved.warnings) != 1 ||
				!strings.HasPrefix(resolved.warnings[0], "current .env skipped:") {
				t.Fatalf("resolved = %#v", resolved)
			}
		})
	}
}

func TestLLMConfigurationAcceptsNonSecretCurrentFileMode(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	working := t.TempDir()
	writeTestLLMEnv(t, filepath.Join(working, llmConfigName), 0o644, strings.Join([]string{
		"MANIUD_LLM_PROVIDER=openai", "MANIUD_LLM_MODEL=current-model", "MANIUD_LLM_TIMEOUT=30", "",
	}, "\n"))
	resolved, err := loadLLMConfiguration(t.Context(), map[string]string{
		homeEnvironment: home, openAIKeyEnvironment: "process-secret",
	}, working)
	if err != nil || resolved.identity == "" || resolved.config.Model != "current-model" || len(resolved.warnings) != 0 {
		t.Fatalf("resolved = %#v, error = %v", resolved, err)
	}
}

func TestLLMConfigurationRejectsSymlinkedXDGAncestor(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	target := filepath.Join(home, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(home, "config")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	resolved, err := loadLLMConfiguration(t.Context(), map[string]string{
		homeEnvironment: home, xdgConfigHomeEnvironment: symlink,
	}, t.TempDir())
	if err != nil || len(resolved.warnings) != 1 ||
		!strings.Contains(resolved.warnings[0], "configuration path is unsafe") {
		t.Fatalf("resolved = %#v, error = %v", resolved, err)
	}
}

//nolint:cyclop // The test proves save, permissions, and explicit key clearing against one persisted file.
func TestLLMConfigurationSavePreservesUnknownAssignmentsAndClearsKey(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	working := t.TempDir()
	environment := map[string]string{
		homeEnvironment: home, xdgConfigHomeEnvironment: filepath.Join(home, "config"),
	}
	assistant := defaultTUIAssistant(environment, working, &tuiDeploymentWorkspace{}, nil)
	if _, err := assistant.Configuration(t.Context()); err != nil {
		t.Fatalf("Configuration() error = %v", err)
	}
	saved, err := assistant.Save(t.Context(), tui.LLMSettings{
		Provider: string(llm.ProviderOpenAI), Model: "gpt-test", Timeout: "60", APIKey: "new-secret",
	})
	if err != nil || !saved.Complete || !saved.KeyConfigured || saved.KeySource != "XDG .env" {
		t.Fatalf("Save() = %#v, %v", saved, err)
	}
	path := filepath.Join(environment[xdgConfigHomeEnvironment], llmConfigDirectory, llmConfigName)
	raw, err := os.ReadFile(path) //nolint:gosec // The path is rooted in this test's private temporary directory.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "OPENAI_API_KEY=new-secret") {
		t.Fatalf("saved content = %q", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved mode = %v", info.Mode())
	}
	if _, err = assistant.Save(t.Context(), tui.LLMSettings{
		Provider: string(llm.ProviderOpenAI), Model: "gpt-test", Timeout: "60", ClearAPIKey: true,
	}); err != nil {
		t.Fatalf("Save(clear) error = %v", err)
	}
	raw, err = os.ReadFile(path) //nolint:gosec // The path is rooted in this test's private temporary directory.
	if err != nil || strings.Contains(string(raw), openAIKeyEnvironment) {
		t.Fatalf("cleared content = %q, error = %v", raw, err)
	}
}

func TestLLMConfigurationSaveRejectsStaleBaselineAndUnsafeLock(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name      string
		configure func(*testing.T, string)
	}{
		{name: "stale", configure: func(t *testing.T, directory string) {
			t.Helper()
			writeTestLLMEnv(t, filepath.Join(directory, llmConfigName), 0o600, "CHANGED=1\n")
		}},
		{name: "symlink lock", configure: func(t *testing.T, directory string) {
			t.Helper()
			target := filepath.Join(t.TempDir(), "lock")
			writeTestLLMEnv(t, target, 0o600, "")
			if err := os.Symlink(target, filepath.Join(directory, llmConfigLockName)); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			home := t.TempDir()
			xdg := filepath.Join(home, "config")
			directory := filepath.Join(xdg, llmConfigDirectory)
			writeTestLLMEnv(t, filepath.Join(directory, llmConfigName), 0o600, validTestLLMEnv("old-secret"))
			environment := map[string]string{homeEnvironment: home, xdgConfigHomeEnvironment: xdg}
			resolved, err := loadLLMConfiguration(t.Context(), environment, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			test.configure(t, directory)
			updates, err := llmSettingsUpdates(tui.LLMSettings{
				Provider: string(llm.ProviderOpenAI), Model: "new-model", Timeout: "60",
			})
			if err != nil {
				t.Fatal(err)
			}
			err = publishXDGLLMEnv(environment, resolved.baseline, updates)
			if test.name == "stale" && !errors.Is(err, errLLMConfigSaveStale) ||
				test.name == "symlink lock" && !errors.Is(err, errLLMConfigPathInvalid) {
				t.Fatalf("publishXDGLLMEnv() error = %v", err)
			}
		})
	}
}

func TestRewriteLLMEnvUsesComposeDotenvSemantics(t *testing.T) {
	t.Parallel()
	model := "next $model"
	key := "key$with-specials"
	candidate, err := rewriteLLMEnv([]byte(strings.Join([]string{
		"KEEP=preserved", "MANIUD_LLM_MODEL=old", "MANIUD_LLM_MODEL=${KEEP}", "",
	}, "\n")), map[string]*string{llmModelEnvironment: &model, openAIKeyEnvironment: &key})
	if err != nil {
		t.Fatalf("rewriteLLMEnv() error = %v", err)
	}
	values, err := parseLLMEnv(candidate)
	if err != nil || values["KEEP"] != "preserved" || values[llmModelEnvironment] != model ||
		values[openAIKeyEnvironment] != key || strings.Count(string(candidate), llmModelEnvironment+"=") != 1 {
		t.Fatalf("candidate = %q, values = %#v, error = %v", candidate, values, err)
	}
	if _, err = rewriteLLMEnv([]byte("MANIUD_LLM_MODEL=\"multi\nline\"\n"),
		map[string]*string{llmModelEnvironment: &model}); !errors.Is(err, errLLMConfigInvalid) {
		t.Fatalf("rewriteLLMEnv(multiline) error = %v", err)
	}
}

func FuzzRewriteLLMEnv(fuzz *testing.F) {
	fuzz.Add("KEEP=preserved\nMANIUD_LLM_MODEL=old\n", "new-model")
	fuzz.Add("", "")
	fuzz.Add("MANIUD_LLM_MODEL=\"unterminated\n", testLLMModelValue)
	fuzz.Fuzz(func(t *testing.T, raw string, model string) {
		candidate, err := rewriteLLMEnv(
			[]byte(raw), map[string]*string{llmModelEnvironment: &model},
		)
		if err != nil {
			return
		}
		if len(candidate) > maximumLLMEnvBytes {
			t.Fatalf("rewrite accepted %d bytes", len(candidate))
		}
		values, parseErr := parseLLMEnv(candidate)
		if parseErr != nil || values[llmModelEnvironment] != model {
			t.Fatalf("candidate = %q, values = %#v, error = %v", candidate, values, parseErr)
		}
	})
}

func writeTestLLMEnv(t *testing.T, path string, mode os.FileMode, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func validTestLLMEnv(key string) string {
	return strings.Join([]string{
		"MANIUD_LLM_PROVIDER=openai", "MANIUD_LLM_MODEL=gpt-test",
		"MANIUD_LLM_TIMEOUT=60", "OPENAI_API_KEY=" + key, "",
	}, "\n")
}

//nolint:cyclop,funlen,gocognit,gocyclo // The table covers each pure parsing and update boundary in the key contract.
func TestLLMConfigurationPureHelperBoundaries(t *testing.T) {
	t.Parallel()
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := loadLLMConfiguration(cancelled, nil, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load error = %v", err)
	}
	secrets := knownLLMSecrets([]llmEnvSource{
		{values: map[string]string{openAIKeyEnvironment: "same", deepSeekKeyEnvironment: "deep"}},
		{values: map[string]string{openAIKeyEnvironment: "same"}},
	})
	if len(secrets) != 2 {
		t.Fatalf("knownLLMSecrets() = %q", secrets)
	}
	if _, err := parseLLMEnv([]byte(strings.Repeat("x", maximumLLMEnvBytes+1))); !errors.Is(err, errLLMConfigInvalid) {
		t.Fatalf("oversized parse error = %v", err)
	}
	for _, environment := range []map[string]string{
		{}, {xdgConfigHomeEnvironment: testRelativePath},
		{xdgConfigHomeEnvironment: "/tmp/../tmp"},
		{xdgConfigHomeEnvironment: "/tmp/invalid\x00root"},
	} {
		if _, err := llmConfigRootPath(environment); !errors.Is(err, errLLMConfigPathInvalid) {
			t.Fatalf("llmConfigRootPath(%q) error = %v", environment, err)
		}
	}
	path, err := llmConfigRootPath(map[string]string{homeEnvironment: "/home/example"})
	if err != nil || path != "/home/example/.config/maniud" {
		t.Fatalf("default root = %q, %v", path, err)
	}

	for _, settings := range []tui.LLMSettings{
		{Provider: string(llm.ProviderDeepSeek), Model: "deepseek-chat", Timeout: "60", APIKey: testLLMKey},
		{Provider: string(llm.ProviderOpenAICompatible), Model: testLLMModelValue,
			Timeout: "60", Endpoint: "https://example.com"},
	} {
		if updates, err := llmSettingsUpdates(settings); err != nil || len(updates) == 0 {
			t.Fatalf("llmSettingsUpdates(%#v) = %#v, %v", settings, updates, err)
		}
	}
	for _, settings := range []tui.LLMSettings{
		{},
		{Provider: string(llm.ProviderOpenAI), Model: testLLMModelValue, Timeout: testInvalidValue},
		{Provider: string(llm.ProviderOpenAI), Model: testLLMModelValue, Timeout: "60", APIKey: "bad key"},
		{Provider: string(llm.ProviderOpenAI), Model: testLLMModelValue,
			Timeout: "60", APIKey: testLLMKey, ClearAPIKey: true},
	} {
		if _, err := llmSettingsUpdates(settings); !errors.Is(err, errLLMConfigInvalid) {
			t.Fatalf("invalid settings %#v error = %v", settings, err)
		}
	}

	value := testLLMValue
	if _, err := rewriteLLMEnv(
		[]byte(strings.Repeat("x", maximumLLMEnvBytes+1)), nil,
	); !errors.Is(err, errLLMConfigInvalid) {
		t.Fatalf("oversized rewrite error = %v", err)
	}
	if _, err := rewriteLLMEnv(nil, map[string]*string{"UNKNOWN": &value}); !errors.Is(err, errLLMConfigInvalid) {
		t.Fatalf("unknown rewrite error = %v", err)
	}
	for _, line := range []string{"", "# comment", testInvalidValue, "1BAD=value"} {
		if _, _, valid := dotenvAssignment(line); valid {
			t.Fatalf("dotenvAssignment(%q) succeeded", line)
		}
	}
	name, assignment, valid := dotenvAssignment("export VALID_NAME = value\n")
	if !valid || name != "VALID_NAME" || assignment != testLLMValue {
		t.Fatalf("export assignment = %q, %q, %t", name, assignment, valid)
	}
	for _, name := range []string{"", "1NAME", "BAD-NAME"} {
		if validDotenvName(name) {
			t.Fatalf("validDotenvName(%q) = true", name)
		}
	}
	for _, value := range []string{"'unterminated", "\"unterminated", "\"escaped\\\""} {
		if singleLineDotenvValue(value) {
			t.Fatalf("singleLineDotenvValue(%q) = true", value)
		}
	}
	if !singleLineDotenvValue("\"escaped\\\"quote\"") {
		t.Fatal("balanced escaped value rejected")
	}

	missing := llmEnvState{}
	if !sameLLMEnvState(missing, missing) || sameLLMEnvState(missing, llmEnvState{exists: true}) {
		t.Fatal("missing state comparison failed")
	}
	existing := llmEnvState{exists: true, valid: true, mode: 0o600, identity: llmFileIdentity{inode: 1}}
	if !sameLLMEnvState(existing, existing) || sameLLMEnvState(existing, llmEnvState{exists: true, valid: true}) {
		t.Fatal("existing state comparison failed")
	}
	configuration := publicLLMConfiguration(llmResolvedConfig{})
	if configuration.Timeout != "" || configuration.Complete {
		t.Fatalf("empty public configuration = %#v", configuration)
	}
	deepSeek, err := loadLLMConfiguration(t.Context(), map[string]string{
		homeEnvironment: t.TempDir(), llmProviderEnvironment: string(llm.ProviderDeepSeek),
		llmModelEnvironment: "deepseek-chat", llmTimeoutEnvironment: "60", deepSeekKeyEnvironment: testLLMKey,
	}, t.TempDir())
	if err != nil || deepSeek.config.APIKey != testLLMKey {
		t.Fatalf("DeepSeek configuration = %#v, %v", deepSeek, err)
	}
	model := testLLMModelValue
	if _, err = rewriteLLMEnv(
		[]byte("BROKEN='unterminated\n"), map[string]*string{llmModelEnvironment: &model},
	); !errors.Is(err, errLLMConfigInvalid) {
		t.Fatalf("malformed preserved dotenv error = %v", err)
	}
}

//nolint:cyclop,funlen,gocognit,gocyclo // Descriptor and path safety outcomes need distinct filesystem fixtures.
func TestLLMConfigurationFilesystemBoundaries(t *testing.T) {
	t.Parallel()
	for _, working := range []string{"", testRelativePath, filepath.Join(t.TempDir(), "missing")} {
		if _, warning := readCurrentLLMEnv(working); warning == "" {
			t.Fatalf("readCurrentLLMEnv(%q) omitted warning", working)
		}
	}
	if warning := llmSourceWarningIfExists("source", false, "reason"); warning != "" {
		t.Fatalf("missing-source warning = %q", warning)
	}
	if warning := llmSourceWarningIfExists("source", true, "reason"); warning == "" {
		t.Fatal("existing-source warning omitted")
	}
	if _, warning := readXDGLLMEnv(map[string]string{}); warning == "" {
		t.Fatal("invalid XDG root omitted warning")
	}
	for _, content := range []string{"", "BROKEN='unterminated\n"} {
		home := t.TempDir()
		directory := filepath.Join(home, "config", llmConfigDirectory)
		if err := os.MkdirAll(directory, llmDirectoryMode); err != nil {
			t.Fatal(err)
		}
		if content != "" {
			writeTestLLMEnv(t, filepath.Join(directory, llmConfigName), llmFileMode, content)
		}
		state, warning := readXDGLLMEnv(map[string]string{
			xdgConfigHomeEnvironment: filepath.Join(home, "config"),
		})
		if content == "" && (!state.valid || warning != "") || content != "" && warning == "" {
			t.Fatalf("XDG content %q = %#v, %q", content, state, warning)
		}
	}

	t.Run("entry types", func(t *testing.T) {
		t.Parallel()
		for _, setup := range []func(*testing.T, string){
			func(t *testing.T, directory string) {
				t.Helper()
				if err := os.Mkdir(filepath.Join(directory, llmConfigName), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, directory string) {
				t.Helper()
				path := filepath.Join(directory, llmConfigName)
				writeTestLLMEnv(t, path, 0o600, "A=1\n")
				if err := os.Link(path, filepath.Join(directory, "second-link")); err != nil {
					t.Fatal(err)
				}
			},
			func(t *testing.T, directory string) {
				t.Helper()
				path := filepath.Join(directory, llmConfigName)
				writeTestLLMEnv(t, path, 0o644, "A=1\n")
			},
			func(t *testing.T, directory string) {
				t.Helper()
				path := filepath.Join(directory, llmConfigName)
				writeTestLLMEnv(t, path, 0o600, "")
				if err := os.Truncate(path, maximumLLMEnvBytes+1); err != nil {
					t.Fatal(err)
				}
			},
		} {
			directory := t.TempDir()
			setup(t, directory)
			root, err := os.OpenRoot(directory)
			if err != nil {
				t.Fatal(err)
			}
			state, reason := readLLMEnvEntry(root, true)
			_ = root.Close()
			if !state.exists || reason == "" {
				t.Fatalf("unsafe entry = %#v, %q", state, reason)
			}
		}
	})

	t.Run("unsafe roots and locks", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		xdg := filepath.Join(home, "config")
		if err := os.WriteFile(xdg, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := openLLMConfigRoot(map[string]string{xdgConfigHomeEnvironment: xdg}, true); err == nil {
			t.Fatal("file root succeeded")
		}

		rootDirectory := t.TempDir()
		writeTestLLMEnv(t, filepath.Join(rootDirectory, llmConfigLockName), 0o644, "")
		root, err := os.OpenRoot(rootDirectory)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = openLLMConfigLock(root); !errors.Is(err, errLLMConfigPathInvalid) {
			t.Fatalf("unsafe lock error = %v", err)
		}
		_ = root.Close()
		if err = syncLLMConfigDirectory(root); err == nil {
			t.Fatal("sync closed directory succeeded")
		}
		if _, err = openLLMConfigLock(root); !errors.Is(err, errLLMConfigPathInvalid) {
			t.Fatalf("lock on closed root error = %v", err)
		}
	})

	t.Run("final directory permissions", func(t *testing.T) {
		t.Parallel()
		home := t.TempDir()
		xdg := filepath.Join(home, "config")
		directory := filepath.Join(xdg, llmConfigDirectory)
		if err := os.MkdirAll(directory, 0o755); err != nil { //nolint:gosec // Unsafe mode is the test input.
			t.Fatal(err)
		}
		if err := os.Chmod(directory, 0o755); err != nil { //nolint:gosec // Unsafe mode is the test input.
			t.Fatal(err)
		}
		if _, err := openLLMConfigRoot(map[string]string{xdgConfigHomeEnvironment: xdg}, false); !errors.Is(
			err, errLLMConfigPathInvalid,
		) {
			t.Fatalf("unsafe directory mode error = %v", err)
		}
	})

	t.Run("current file unsafe permissions", func(t *testing.T) {
		t.Parallel()
		directory := t.TempDir()
		writeTestLLMEnv(t, filepath.Join(directory, llmConfigName), 0o622, "A=1\n")
		root, err := os.OpenRoot(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer root.Close() //nolint:errcheck // Test cleanup only.
		if _, reason := readLLMEnvEntry(root, false); reason != "entry permissions are unsafe" {
			t.Fatalf("unsafe current permissions reason = %q", reason)
		}
	})

	if err := publishXDGLLMEnv(nil, llmConfigBaseline{}, nil); !errors.Is(err, errLLMConfigSaveStale) {
		t.Fatalf("uninitialized publish error = %v", err)
	}
	if err := publishXDGLLMEnv(nil, llmConfigBaseline{initialized: true}, nil); !errors.Is(err, errLLMConfigSaveStale) {
		t.Fatalf("invalid baseline publish error = %v", err)
	}
	if err := publishXDGLLMEnv(
		map[string]string{xdgConfigHomeEnvironment: testRelativePath},
		llmConfigBaseline{initialized: true, state: llmEnvState{valid: true}}, nil,
	); !errors.Is(err, errLLMConfigPathInvalid) {
		t.Fatalf("unsafe publish root error = %v", err)
	}
	if identity, valid := llmIdentity(fakeLLMFileInfo{}); valid || identity != (llmFileIdentity{}) {
		t.Fatalf("unsupported identity = %#v, %t", identity, valid)
	}
}

type fakeLLMFileInfo struct{}

func (fakeLLMFileInfo) Name() string       { return "fake" }
func (fakeLLMFileInfo) Size() int64        { return 0 }
func (fakeLLMFileInfo) Mode() os.FileMode  { return 0 }
func (fakeLLMFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeLLMFileInfo) IsDir() bool        { return false }
func (fakeLLMFileInfo) Sys() any           { return nil }

//nolint:funlen // Every injected read and anchored-descent fault has a distinct rejection point.
func TestLLMConfigurationInjectedReadAndRootFaults(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	writeTestLLMEnv(t, filepath.Join(directory, llmConfigName), llmFileMode, "A=1\n")
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	readFaults := []func(*llmConfigOperations){
		func(operations *llmConfigOperations) {
			operations.openFile = func(*os.Root, string, int, os.FileMode) (*os.File, error) {
				return nil, errLLMConfigFixture
			}
		},
		func(operations *llmConfigOperations) {
			operations.readFile = func(*os.File) ([]byte, error) { return nil, errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			operations.statFile = func(*os.File) (os.FileInfo, error) { return nil, errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			base := operations.closeFile
			operations.closeFile = func(file *os.File) error {
				_ = base(file)

				return errLLMConfigFixture
			}
		},
		func(operations *llmConfigOperations) {
			base := operations.lstat
			calls := 0
			operations.lstat = func(root *os.Root, name string) (os.FileInfo, error) {
				calls++
				if calls == 2 {
					return nil, errLLMConfigFixture
				}

				return base(root, name)
			}
		},
		func(operations *llmConfigOperations) {
			operations.readFile = func(*os.File) ([]byte, error) {
				return make([]byte, maximumLLMEnvBytes+1), nil
			}
		},
		func(operations *llmConfigOperations) {
			operations.sameFile = func(os.FileInfo, os.FileInfo) bool { return false }
		},
	}
	for index, mutate := range readFaults {
		operations := defaultLLMConfigOperations()
		mutate(&operations)
		if _, reason := readLLMEnvEntryWithOperations(root, true, operations); reason == "" {
			t.Fatalf("read fault %d succeeded", index)
		}
	}

	environment, _ := llmFaultConfiguration(t)
	rootFaults := []func(*llmConfigOperations){
		func(operations *llmConfigOperations) {
			operations.openRoot = func(string) (*os.Root, error) { return nil, errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			operations.lstat = func(*os.Root, string) (os.FileInfo, error) { return nil, errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			operations.openSubRoot = func(*os.Root, string) (*os.Root, error) { return nil, errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			operations.statRoot = func(*os.Root, string) (os.FileInfo, error) { return nil, errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			operations.sameFile = func(os.FileInfo, os.FileInfo) bool { return false }
		},
		func(operations *llmConfigOperations) {
			base := operations.closeRoot
			calls := 0
			operations.closeRoot = func(root *os.Root) error {
				calls++
				err := base(root)
				if calls == 1 {
					return errors.Join(err, errLLMConfigFixture)
				}

				return err
			}
		},
	}
	for index, mutate := range rootFaults {
		operations := defaultLLMConfigOperations()
		mutate(&operations)
		if opened, openErr := openLLMConfigRootWithOperations(environment, false, operations); openErr == nil {
			_ = opened.Close()
			t.Fatalf("root fault %d succeeded", index)
		}
	}

	missingEnvironment := map[string]string{xdgConfigHomeEnvironment: filepath.Join(t.TempDir(), "missing")}
	operations := defaultLLMConfigOperations()
	operations.mkdir = func(*os.Root, string, os.FileMode) error { return errLLMConfigFixture }
	if _, err = openLLMConfigRootWithOperations(missingEnvironment, true, operations); err == nil {
		t.Fatal("mkdir fault succeeded")
	}
}

//nolint:cyclop,funlen,gocognit // Atomic publication faults prove each before/after-rename error classification.
func TestLLMConfigurationInjectedPublicationFaults(t *testing.T) {
	t.Parallel()
	faults := []func(*llmConfigOperations){
		func(operations *llmConfigOperations) {
			base := operations.lstat
			operations.lstat = func(root *os.Root, name string) (os.FileInfo, error) {
				if name == llmConfigLockName {
					return nil, errLLMConfigFixture
				}

				return base(root, name)
			}
		},
		func(operations *llmConfigOperations) {
			base := operations.openFile
			operations.openFile = func(root *os.Root, name string, flag int, mode os.FileMode) (*os.File, error) {
				if name == llmConfigLockName {
					return nil, errLLMConfigFixture
				}

				return base(root, name, flag, mode)
			}
		},
		func(operations *llmConfigOperations) {
			operations.flock = func(int, int) error { return errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			operations.sameFile = func(left, right os.FileInfo) bool {
				if left.Name() == llmConfigLockName || right.Name() == llmConfigLockName {
					return false
				}

				return os.SameFile(left, right)
			}
		},
		func(operations *llmConfigOperations) {
			base := operations.openFile
			operations.openFile = func(root *os.Root, name string, flag int, mode os.FileMode) (*os.File, error) {
				if strings.HasPrefix(name, ".env.maniud-") {
					return nil, errLLMConfigFixture
				}

				return base(root, name, flag, mode)
			}
		},
		func(operations *llmConfigOperations) {
			operations.writeFile = func(*os.File, []byte) (int, error) { return 0, errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			operations.writeFile = func(*os.File, []byte) (int, error) { return 0, nil }
		},
		func(operations *llmConfigOperations) {
			base := operations.syncFile
			operations.syncFile = func(file *os.File) error {
				if strings.Contains(filepath.Base(file.Name()), ".env.maniud-") {
					return errLLMConfigFixture
				}

				return base(file)
			}
		},
		func(operations *llmConfigOperations) {
			base := operations.closeFile
			operations.closeFile = func(file *os.File) error {
				err := base(file)
				if strings.Contains(filepath.Base(file.Name()), ".env.maniud-") {
					return errors.Join(err, errLLMConfigFixture)
				}

				return err
			}
		},
		func(operations *llmConfigOperations) {
			base := operations.readFile
			calls := 0
			operations.readFile = func(file *os.File) ([]byte, error) {
				calls++
				if calls == 2 {
					return []byte(validTestLLMEnv("changed-before-publish")), nil
				}

				return base(file)
			}
		},
		func(operations *llmConfigOperations) {
			operations.rename = func(*os.Root, string, string) error { return errLLMConfigFixture }
		},
		func(operations *llmConfigOperations) {
			base := operations.readFile
			calls := 0
			operations.readFile = func(file *os.File) ([]byte, error) {
				calls++
				if calls == 3 {
					return []byte(validTestLLMEnv("wrong-after-publish")), nil
				}

				return base(file)
			}
		},
		func(operations *llmConfigOperations) {
			operations.openDirectory = func(*os.Root, string) (*os.File, error) {
				return nil, errLLMConfigFixture
			}
		},
	}
	for index, mutate := range faults {
		environment, baseline := llmFaultConfiguration(t)
		operations := defaultLLMConfigOperations()
		mutate(&operations)
		model := testLLMChangedModel
		err := publishXDGLLMEnvWithOperations(
			environment, baseline, map[string]*string{llmModelEnvironment: &model}, operations,
		)
		if err == nil {
			t.Fatalf("publication fault %d succeeded", index)
		}
	}

	for _, persistent := range []bool{false, true} {
		environment, baseline := llmFaultConfiguration(t)
		operations := defaultLLMConfigOperations()
		base := operations.openDirectory
		calls := 0
		operations.openDirectory = func(root *os.Root, name string) (*os.File, error) {
			calls++
			if calls == 1 || persistent {
				return nil, errLLMConfigFixture
			}

			return base(root, name)
		}
		model := testLLMChangedModel
		err := publishXDGLLMEnvWithOperations(
			environment, baseline, map[string]*string{llmModelEnvironment: &model}, operations,
		)
		if persistent && !errors.Is(err, errLLMConfigSaveUnknown) || !persistent && err != nil {
			t.Fatalf("directory settle persistent=%t error = %v", persistent, err)
		}
	}

	environment, baseline := llmFaultConfiguration(t)
	invalid := testLLMValue
	if err := publishXDGLLMEnvWithOperations(
		environment, baseline, map[string]*string{"INVALID": &invalid}, defaultLLMConfigOperations(),
	); !errors.Is(err, errLLMConfigInvalid) {
		t.Fatalf("invalid rewrite error = %v", err)
	}
}

func llmFaultConfiguration(t *testing.T) (map[string]string, llmConfigBaseline) {
	t.Helper()
	home := t.TempDir()
	xdg := filepath.Join(home, "config")
	directory := filepath.Join(xdg, llmConfigDirectory)
	writeTestLLMEnv(t, filepath.Join(directory, llmConfigName), llmFileMode, validTestLLMEnv(testLLMKey))
	environment := map[string]string{homeEnvironment: home, xdgConfigHomeEnvironment: xdg}
	resolved, err := loadLLMConfiguration(t.Context(), environment, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return environment, resolved.baseline
}
