package changescope

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupMigratesLoginProfileWithoutLosingToolPath(t *testing.T) {
	t.Parallel()
	contents, err := os.ReadFile("../../.agents/setup")
	if err != nil {
		t.Fatal(err)
	}
	const declaration = "configure_mise_login_shell() {\n"
	_, body, found := strings.Cut(string(contents), declaration)
	body, _, ended := strings.Cut(body, "\n}\n")
	if !found || !ended {
		t.Fatal("setup profile function is missing")
	}
	script := "set -eu\n" + declaration + body + "\n}\nconfigure_mise_login_shell\n"
	const existing = `export KEEP=original

# maniud mise activation
if [[ -x "$HOME/.local/bin/mise" ]]; then
  eval "$("$HOME/.local/bin/mise" activate bash)"
fi
`
	const wantProfile = `export KEEP=original

# maniud mise activation
if [[ -x "$HOME/.local/bin/mise" ]]; then
  eval "$("$HOME/.local/bin/mise" activate bash)"
  eval "$("$HOME/.local/bin/mise" hook-env -s bash)"
fi
`
	directory := t.TempDir()
	write(t, directory, ".bash_profile", existing)
	write(t, directory, ".local/bin/mise", `#!/usr/bin/env bash
case "$*" in
  'activate bash') printf ':\n' ;;
  'hook-env -s bash') printf 'export PATH="$HOME/tools:$PATH"\n' ;;
  *) exit 91 ;;
esac
`)
	write(t, directory, "tools/maniud-profile-probe", "#!/usr/bin/env bash\nprintf 'fixture-tool\\n'\n")
	run(t, directory, "chmod", "700", ".local/bin/mise", "tools/maniud-profile-probe")
	environment := append(os.Environ(), "HOME="+directory, "BASH_ENV=", "ENV=")
	for range 2 {
		//nolint:gosec // Execute only the repository-owned profile function in an isolated HOME.
		command := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c", script)
		command.Dir, command.Env = directory, environment
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("profile migration: %v\n%s", err, output)
		}
		//nolint:gosec // Read only the fixed profile filename under this test's temporary HOME.
		profile, err := os.ReadFile(filepath.Join(directory, ".bash_profile"))
		if err != nil || string(profile) != wantProfile {
			t.Fatalf("migrated profile = %q, error %v", profile, err)
		}
	}
	command := exec.CommandContext(t.Context(), "bash", "--noprofile", "--norc", "-c",
		"set -eu; source \"$HOME/.bash_profile\"; maniud-profile-probe; printf '%s\\n' \"$KEEP\"")
	command.Dir, command.Env = directory, environment
	if output, err := command.CombinedOutput(); err != nil || string(output) != "fixture-tool\noriginal\n" {
		t.Fatalf("noninteractive profile tools = %q, error %v", output, err)
	}
}
