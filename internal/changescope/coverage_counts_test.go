package changescope

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const coverageCountGoFixture = `#!/bin/sh
case "$1" in
list)
  if [ "$2" = -m ]; then
    printf 'example.test/root\n'
  elif [ "$2" = -f ]; then
    if [ "$3" = '{{.Dir}}' ]; then
      printf '%s\n' "$PWD"
    else
      printf '%s\tfixture.go\n' "$PWD"
    fi
  else
    printf 'example.test/root\n'
  fi
  ;;
test)
  for argument do
    case "$argument" in
    -coverprofile=*) cp input.cover "${argument#-coverprofile=}" ;;
    esac
  done
  ;;
tool) printf 'total: (statements) 100.0%%\n' ;;
*) exit 91 ;;
esac
`

func TestCoverageUsesExactMergedStatementCounts(t *testing.T) {
	t.Parallel()
	const coverageScript = "scripts/check-go-coverage"
	for _, test := range []struct {
		name    string
		profile string
		want    string
	}{
		{name: "rounded incomplete", want: "covered statements are 9999/10000, want 10000/10000",
			profile: "example.test/root/fixture.go:1.1,1.2 9999 1\nexample.test/root/fixture.go:2.1,2.2 1 0\n"},
		{name: "merged duplicate", want: "covered branch outcomes are 2/2",
			profile: "example.test/root/fixture.go:1.1,1.2 1 1\nexample.test/root/fixture.go:1.1,1.2 1 0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			copyFile(t, "../../scripts/check-go-coverage", filepath.Join(root, coverageScript))
			write(t, root, "scripts/list-go-modules", "#!/bin/sh\nprintf '.\\n'\n")
			write(t, root, "go.mod", "module example.test/root\n\ngo 1.27.0\n")
			write(t, root, "fixture.go", "package fixture\n")
			write(t, root, "input.cover", "mode: atomic\n"+test.profile)
			write(t, root, "bin/go", coverageCountGoFixture)
			write(t, root, "bin/gobco", "#!/bin/sh\nprintf '[{\"TrueCount\":1,\"FalseCount\":1}]' >\"$3\"\n")
			run(t, root, "chmod", "700", "scripts/list-go-modules", "bin/go", "bin/gobco")
			command := exec.CommandContext(t.Context(), "bash", coverageScript)
			command.Dir = root
			command.Env = append(os.Environ(), "PATH="+filepath.Join(root, "bin")+":"+os.Getenv("PATH"),
				"MANIUD_GATE_MANIFEST=")
			output, err := command.CombinedOutput()
			if (err != nil) != (test.name == "rounded incomplete") || !strings.Contains(string(output), test.want) {
				t.Fatalf("coverage = %v\n%s", err, output)
			}
		})
	}
}
