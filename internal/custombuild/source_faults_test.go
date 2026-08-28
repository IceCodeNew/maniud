package custombuild

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestResolveSourceRootWithReportsPathFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		absolute func(string) (string, error)
		evaluate func(string) (string, error)
	}{
		{
			name:     "absolute",
			absolute: func(string) (string, error) { return "", errCustomBuildTest },
			evaluate: func(value string) (string, error) { return value, nil },
		},
		{
			name:     "symlinks",
			absolute: func(value string) (string, error) { return value, nil },
			evaluate: func(string) (string, error) { return "", errCustomBuildTest },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := resolveSourceRootWith("source", test.absolute, test.evaluate); !errors.Is(err, errCustomBuildTest) {
				t.Fatalf("resolveSourceRootWith() error = %v", err)
			}
		})
	}
}

func TestResolveConfigValidatesSourceOutputTargetAndRuntimes(t *testing.T) {
	t.Parallel()

	root := validModuleTree(t)
	absoluteOutput := filepath.Join(t.TempDir(), testOutputFilename)
	resolved, err := resolveConfig(Config{
		Root: root, Output: absoluteOutput, Target: testLinuxARM64,
		Runtimes: []string{containerdRuntime, dockerRuntime},
	})
	if err != nil || resolved.output != absoluteOutput || resolved.target != testLinuxARM64 ||
		!slices.Equal(resolved.runtimes, []string{dockerRuntime, containerdRuntime}) {
		t.Fatalf("resolveConfig() = %#v, %v", resolved, err)
	}

	if _, err = resolveConfig(Config{}); !errors.Is(err, errInvalidConfiguration) {
		t.Fatalf("resolveConfig(empty) error = %v", err)
	}
	brokenModules := validModuleTree(t)
	if err = os.Remove(filepath.Join(brokenModules, "argv", "go.mod")); err != nil {
		t.Fatal(err)
	}
	tests := []Config{
		{Root: filepath.Join(root, "missing"), Output: testOutputFilename},
		{Root: t.TempDir(), Output: testOutputFilename},
		{Root: brokenModules, Output: testOutputFilename},
		{Root: root, Output: testOutputFilename, Target: "unsupported"},
		{Root: root, Output: testOutputFilename, Target: "linux/386"},
		{Root: root, Output: testOutputFilename, Target: "windows/amd64"},
		{Root: root, Output: testOutputFilename, Runtimes: []string{testUnknownRuntime}},
	}
	for _, config := range tests {
		if _, err = resolveConfig(config); err == nil {
			t.Fatalf("resolveConfig(%#v) unexpectedly succeeded", config)
		}
	}
}

func TestValidateSourceModulesRejectsUnreadableAndMismatchedModules(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	modules := []localModule{{path: projectModule, directory: "."}}
	if err := validateSourceModules(root, modules); err == nil {
		t.Fatal("validateSourceModules(missing) unexpectedly succeeded")
	}
	path := filepath.Join(root, "go.mod")
	if err := os.WriteFile(path, []byte("module example.com/wrong\n"), generatedFileMode); err != nil {
		t.Fatal(err)
	}
	if err := validateSourceModules(root, modules); !errors.Is(err, errInvalidSource) {
		t.Fatalf("validateSourceModules(mismatch) error = %v", err)
	}
}

func validModuleTree(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	source, err := inspectSourceModule(testRepositoryRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	modules := source.modules
	var rootModule strings.Builder
	rootModule.WriteString("module " + projectModule + "\n\ngo " + testGoDirective + "\n\nreplace (\n")
	for _, module := range modules[1:] {
		rootModule.WriteString("\t" + module.path + " => ./" + filepath.ToSlash(module.directory) + "\n")
	}
	rootModule.WriteString("\t" + notificationModule + " => " + notificationFork + " " +
		testNotificationVersion + "\n")
	rootModule.WriteString(")\n")
	for _, module := range modules {
		directory := filepath.Join(root, module.directory)
		if err := os.MkdirAll(directory, outputDirectoryMode); err != nil {
			t.Fatal(err)
		}
		content := "module " + module.path + "\n"
		if module.directory == "." {
			content = rootModule.String()
		}
		if err := os.WriteFile(
			filepath.Join(directory, "go.mod"), []byte(content), generatedFileMode,
		); err != nil {
			t.Fatal(err)
		}
	}

	return root
}

func localModuleDefinitionTests() []struct {
	name    string
	content string
	want    error
} {
	return []struct {
		name    string
		content string
		want    error
	}{
		{name: "missing", want: os.ErrNotExist},
		{name: "root", content: "module example.com/wrong\n", want: errInvalidSource},
		{
			name: "malformed replacement", content: "module " + projectModule + "\ngo " + testGoDirective +
				"\nreplace => ./bad\n", want: errInvalidSource,
		},
		{
			name: "outside", content: "module " + projectModule + "\nreplace " + projectModule +
				"/outside => ./../outside\n", want: errInvalidSource,
		},
		{
			name: "nonlocal", content: "module " + projectModule + "\nreplace " + projectModule +
				"/outside => ../outside\n", want: errInvalidSource,
		},
		{
			name: "remote", content: "module " + projectModule + "\nreplace " + projectModule +
				"/outside => example.com/outside\n", want: errInvalidSource,
		},
		{
			name: "root replacement", content: "module " + projectModule + "\nreplace " + projectModule +
				"/outside => ./\n", want: errInvalidSource,
		},
		{
			name: "versioned local module", content: "module " + projectModule + "\nreplace " + projectModule +
				"/argv v1.0.0 => ./argv\n", want: errInvalidSource,
		},
		{
			name: "wrong notification fork", content: "module " + projectModule + "\ngo " + testGoDirective +
				"\nreplace " + notificationModule + " => example.com/notify " + testNotificationVersion + "\n",
			want: errInvalidSource,
		},
		{
			name: "duplicate", content: "module " + projectModule + "\nreplace (\n" +
				projectModule + "/argv => ./argv\n" + projectModule + "/argv => ./other\n)\n",
			want: errInvalidSource,
		},
		{
			name: "single and block", content: "module " + projectModule + "\ngo " + testGoDirective +
				"\nreplace " +
				projectModule + "/argv => ./argv\nreplace (\n" + projectModule +
				"/imageref => ./imageref\nexample.com/other => ./other\n" + notificationModule + " => " + notificationFork +
				" " + testNotificationVersion + "\n)\n",
		},
	}
}

func TestLocalModuleDefinitionsValidatesRootReplacements(t *testing.T) {
	t.Parallel()

	for _, test := range localModuleDefinitionTests() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root, err := resolveSourceRoot(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, directory := range []string{"argv", "imageref", "other"} {
				if err := os.Mkdir(filepath.Join(root, directory), outputDirectoryMode); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "go.mod")
			if test.content == "" {
				_ = os.Remove(path)
			} else if err := os.WriteFile(path, []byte(test.content), generatedFileMode); err != nil {
				t.Fatal(err)
			}
			source, err := inspectSourceModule(root)
			if !errors.Is(err, test.want) {
				t.Fatalf("inspectSourceModule() = %#v, %v, want %v", source, err, test.want)
			}
			if err == nil && !slices.Equal(source.modules, []localModule{
				{path: projectModule, directory: "."},
				{path: projectModule + "/argv", directory: "argv"},
				{path: projectModule + "/imageref", directory: "imageref"},
			}) {
				t.Fatalf("inspectSourceModule() = %#v", source)
			}
		})
	}
}

func TestInspectSourceModuleRejectsEscapingLocalModuleLink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "argv")); err != nil {
		t.Fatal(err)
	}
	content := "module " + projectModule + "\n\ngo " + testGoDirective + "\n\nreplace (\n\t" +
		projectModule + "/argv => ./argv\n\t" + notificationModule + " => " + notificationFork + " " +
		testNotificationVersion + "\n)\n"
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(content), generatedFileMode); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectSourceModule(root); !errors.Is(err, errInvalidSource) {
		t.Fatalf("inspectSourceModule(escaping link) error = %v", err)
	}
}

func TestInspectSourceWithRunnerValidatesGitEvidence(t *testing.T) {
	t.Parallel()

	validRevision := []byte("0123456789abcdef\n")
	validDate := []byte("20260826\n")
	tests := []struct {
		name    string
		outputs [][]byte
		errors  []error
		want    error
		dirty   bool
	}{
		{name: "revision command", errors: []error{errCustomBuildTest}, want: errCustomBuildTest},
		{name: "short revision", outputs: [][]byte{[]byte("123\n")}, want: errInvalidSource},
		{name: "invalid revision", outputs: [][]byte{[]byte("gggggggggggg\n")}, want: errInvalidSource},
		{
			name: "date command", outputs: [][]byte{validRevision},
			errors: []error{nil, errCustomBuildTest}, want: errCustomBuildTest,
		},
		{name: "invalid date", outputs: [][]byte{validRevision, []byte("2026\n")}, want: errInvalidSource},
		{
			name: "status command", outputs: [][]byte{validRevision, validDate},
			errors: []error{nil, nil, errCustomBuildTest}, want: errCustomBuildTest,
		},
		{name: "clean", outputs: [][]byte{validRevision, validDate, nil}},
		{name: "dirty", outputs: [][]byte{validRevision, validDate, []byte(" M file\n")}, dirty: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			metadata, err := inspectSourceWithRunner(
				t.Context(), t.TempDir(), sequenceCommandRunner(test.outputs, test.errors),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("inspectSourceWithRunner() error = %v, want %v", err, test.want)
			}
			if err == nil && (metadata.modified != test.dirty || strings.Contains(metadata.version, "-dirty") != test.dirty) {
				t.Fatalf("inspectSourceWithRunner() = %#v", metadata)
			}
		})
	}
}

func TestInspectGoToolchainWithRunnerValidatesVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings moduleSettings
		outputs  [][]byte
		errors   []error
		want     error
	}{
		{name: "source version", settings: moduleSettings{goDirective: "invalid"}, want: errInvalidSource},
		{
			name: "version command", settings: moduleSettings{goDirective: testGoDirective},
			errors: []error{errCustomBuildTest}, want: errCustomBuildTest,
		},
		{
			name: "version", settings: moduleSettings{goDirective: testGoDirective},
			outputs: [][]byte{[]byte("go1.26.0\n")}, want: errInvalidConfiguration,
		},
		{
			name: "toolchain directive", settings: moduleSettings{goDirective: "1.26.0", toolchain: testGoVersion},
			outputs: [][]byte{[]byte(testGoVersion + "\n")},
		},
		{
			name: testSuccess, settings: moduleSettings{goDirective: testGoDirective},
			outputs: [][]byte{[]byte(testGoVersion + "\n")},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			version, err := inspectGoToolchainWithRunner(
				t.Context(), t.TempDir(), test.settings, sequenceCommandRunner(test.outputs, test.errors),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("inspectGoToolchainWithRunner() = %q, %v, want %v", version, err, test.want)
			}
		})
	}
}

func TestInspectSourceModuleRejectsMissingVersions(t *testing.T) {
	t.Parallel()

	if _, err := inspectSourceModule(t.TempDir()); err == nil {
		t.Fatal("inspectSourceModule(missing) unexpectedly succeeded")
	}
	root := t.TempDir()
	path := filepath.Join(root, "go.mod")
	if err := os.WriteFile(
		path, []byte("module "+projectModule+"\ngo "+testGoDirective+"\n"), generatedFileMode,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectSourceModule(root); !errors.Is(err, errInvalidSource) {
		t.Fatalf("inspectSourceModule(incomplete) error = %v", err)
	}
}

func TestInspectSourceModuleAcceptsReplaceFormats(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		replacement string
	}{
		{name: "block", replacement: "replace (\n\t" + notificationModule + " => " + notificationFork +
			" " + testNotificationVersion + "\n)\n"},
		{name: "single line", replacement: "replace " + notificationModule + " => " + notificationFork +
			" " + testNotificationVersion + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			content := "module " + projectModule + "\n\ngo " + testGoDirective + "\n\ntoolchain " + testGoVersion +
				"\n\n" + test.replacement
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(content), generatedFileMode); err != nil {
				t.Fatal(err)
			}
			source, err := inspectSourceModule(root)
			if err != nil || source.settings.goDirective != testGoDirective ||
				source.settings.toolchain != testGoVersion ||
				source.settings.notificationVersion != testNotificationVersion {
				t.Fatalf("inspectSourceModule() = %#v, %v", source, err)
			}
		})
	}
}

func TestWriteBuildModuleWithWriterReportsEachWriteFailure(t *testing.T) {
	t.Parallel()

	plan := buildPlan{
		config:      resolvedConfig{root: testRepositoryRoot(t), runtimes: []string{dockerRuntime}},
		goDirective: testGoDirective, toolchain: testGoVersion, notificationVersion: testNotificationVersion,
	}
	tests := []struct {
		name     string
		failCall int
	}{
		{name: "module", failCall: 1},
		{name: "entrypoint", failCall: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			calls := 0
			err := writeBuildModuleWithWriter(
				t.TempDir(), plan,
				func(_ string, content []byte, mode os.FileMode) error {
					calls++
					if calls == 1 && !strings.Contains(string(content), "toolchain "+testGoVersion) {
						t.Error("generated module omitted toolchain directive")
					}
					if mode != generatedFileMode {
						t.Errorf("generated file mode = %o", mode)
					}
					if calls == test.failCall {
						return errCustomBuildTest
					}

					return nil
				},
			)
			if !errors.Is(err, errCustomBuildTest) {
				t.Fatalf("writeBuildModuleWithWriter() error = %v", err)
			}
		})
	}
}

func TestSourceTokenValidatorsRejectInvalidValues(t *testing.T) {
	t.Parallel()

	if validHex("0123456789ag") {
		t.Fatal("validHex() accepted a non-hexadecimal value")
	}
	if validTargetToken("linux-") {
		t.Fatal("validTargetToken() accepted punctuation")
	}
}
