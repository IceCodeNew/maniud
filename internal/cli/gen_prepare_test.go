package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const bindPreparationTestTarget = "/data"

func TestExecuteGenWritesBindPreparationScript(t *testing.T) {
	t.Parallel()

	platform := defaultGeneratedPlatform()
	resolver := &genResolverFixture{image: generatedImage(t, platform)}
	var generated generatedCompose
	output := new(bytes.Buffer)
	instructions := new(bytes.Buffer)
	err := executeGen(context.Background(), genInvocation{
		runtimeArgs: []string{
			testDockerRuntime, createOperation, "--user=" + testNumericUser,
			"--volume=/srv/example/data:/data", testRegistrySource,
		},
		name: testServiceName, output: "generated/service.yaml", json: true,
	}, output, genDependencies{
		workingDirectory:  testWorkingDirectory,
		images:            resolver,
		probeRuntimeImage: observedGeneratedImage,
		write: func(value generatedCompose) error {
			generated = value

			return nil
		},
		instructions: instructions,
	})
	if err != nil {
		t.Fatalf("executeGen() error = %v", err)
	}
	if generated.preparationPath != "generated/service.prepare.sh" ||
		generated.preparationAbsolute != testWorkingDirectory+"/generated/service.prepare.sh" ||
		!bytes.Contains(generated.content, []byte("source: /srv/example/data")) ||
		!bytes.Contains(generated.preparation, []byte("uid='1000'\ngid='1001'")) {
		t.Fatalf("executeGen() generated = %#v", generated)
	}
	wantOutput := `{"path":"generated/service.yaml","status":"generated",` +
		`"prepare_script":"generated/service.prepare.sh"}` + "\n"
	if output.String() != wantOutput {
		t.Fatalf("executeGen() output = %q", output.String())
	}
	if instructions.String() != "maniud: review and run the generated preparation script before maniud apply.\n" {
		t.Fatalf("executeGen() instructions = %q", instructions.String())
	}
}

func TestRenderBindPreparationCanonicalizesOwnerAndPaths(t *testing.T) {
	t.Parallel()

	script, err := renderBindPreparation(domain.WorkloadSpec{
		User: "001000:001001",
		Mounts: []domain.Mount{
			{Kind: domain.MountBind, Source: "/srv/z", Target: "/z"},
			{Kind: domain.MountVolume, Target: "/volume"},
			{Kind: domain.MountBind, Source: "/srv/a'b", Target: "/quoted"},
			{Kind: domain.MountBind, Source: "/srv/z", Target: "/again"},
		},
	})
	if err != nil {
		t.Fatalf("renderBindPreparation() error = %v", err)
	}
	text := string(script)
	if !strings.Contains(text, "uid='1000'\ngid='1001'") ||
		strings.Count(text, "prepare_bind directory '/srv/z'") != 1 ||
		!strings.Contains(text, `prepare_bind directory '/srv/a'"'"'b'`) ||
		strings.Index(text, "/srv/a") > strings.Index(text, "/srv/z") {
		t.Fatalf("renderBindPreparation() = %q", text)
	}
}

func TestPreparationOwnerBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		user  string
		uid   string
		gid   string
		valid bool
	}{
		{user: "", uid: rootPreparationID, gid: rootPreparationID, valid: true},
		{user: testNumericUser, uid: "'1000'", gid: "'1001'", valid: true},
		{user: "1000"},
		{user: "service:1001"},
		{user: "service:group"},
		{user: "4294967296:1"},
	}
	for _, test := range tests {
		uid, gid, err := preparationOwner(test.user)
		if (err == nil) != test.valid || uid != test.uid || gid != test.gid {
			t.Fatalf("preparationOwner(%q) = %q, %q, %v", test.user, uid, gid, err)
		}
	}
}

func TestRenderBindPreparationRejectsNamedOwner(t *testing.T) {
	t.Parallel()

	_, err := renderBindPreparation(domain.WorkloadSpec{
		User: testServiceName,
		Mounts: []domain.Mount{{
			Kind: domain.MountBind, Source: "/srv/data", Target: bindPreparationTestTarget,
		}},
	})
	if !errors.Is(err, errBindPreparationOwner) ||
		err.Error() != errBindPreparationOwner.Error() {
		t.Fatalf("renderBindPreparation(named owner) error = %v", err)
	}
}

func TestRenderGeneratedBindPreparationContainsResolverFailures(t *testing.T) {
	t.Parallel()

	projection, image, workload := generatedBindPreparationFixture(t)
	var err error
	if _, err = renderGeneratedBindPreparation(
		t.Context(), projection, image, workload, genDependencies{},
	); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("renderGeneratedBindPreparation(no resolver) = %v", err)
	}
	if _, err = renderGeneratedBindPreparation(
		t.Context(), projection, image, workload, genDependencies{
			resolveImageUser: func(
				context.Context, domain.RuntimeKind, domain.ImageIdentity, string,
			) (string, error) {
				return "", errGeneratedComposeTest
			},
		},
	); !errors.Is(err, errGeneratedComposeTest) {
		t.Fatalf("renderGeneratedBindPreparation(resolver failure) = %v", err)
	}
}

func TestRenderGeneratedBindPreparationResolvesImageAccount(t *testing.T) {
	t.Parallel()

	projection, image, workload := generatedBindPreparationFixture(t)
	resolved, err := renderGeneratedBindPreparation(
		t.Context(), projection, image, workload, genDependencies{
			resolveImageUser: func(
				_ context.Context,
				runtimeKind domain.RuntimeKind,
				expected domain.ImageIdentity,
				specification string,
			) (string, error) {
				if runtimeKind != domain.RuntimeDocker || expected.Reference != image.Reference ||
					specification != testServiceName {
					t.Fatalf("resolveImageUser() = %s, %#v, %q", runtimeKind, expected, specification)
				}

				return testNumericUser, nil
			},
		},
	)
	if err != nil || !bytes.Contains(resolved, []byte("uid='1000'\ngid='1001'")) {
		t.Fatalf("renderGeneratedBindPreparation() = %q, %v", resolved, err)
	}

	withoutBinds, err := renderGeneratedBindPreparation(
		t.Context(), projection, image, domain.WorkloadSpec{User: testServiceName}, genDependencies{},
	)
	if err != nil || withoutBinds != nil {
		t.Fatalf("renderGeneratedBindPreparation(no binds) = %q, %v", withoutBinds, err)
	}
}

func generatedBindPreparationFixture(
	t *testing.T,
) (runtimeargv.Projection, domain.ImageIdentity, domain.WorkloadSpec) {
	t.Helper()

	projection, err := runtimeargv.Parse(
		[]string{testDockerRuntime, createOperation, testRegistrySource}, "", testWorkingDirectory,
	)
	if err != nil {
		t.Fatal(err)
	}
	workload := domain.WorkloadSpec{
		User: testServiceName,
		Mounts: []domain.Mount{{
			Kind: domain.MountBind, Source: "/srv/data", Target: bindPreparationTestTarget,
		}},
	}

	return projection, generatedImage(t, defaultGeneratedPlatform()), workload
}

func TestRenderBindPreparationRejectsUnsafeSources(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"relative", "/srv/../data", string(filepath.Separator), "/srv/$data", "/srv/data\x00invalid",
	} {
		_, err := renderBindPreparation(domain.WorkloadSpec{Mounts: []domain.Mount{{
			Kind: domain.MountBind, Source: source, Target: bindPreparationTestTarget,
		}}})
		if !errors.Is(err, runtimeargv.ErrInvalid) {
			t.Fatalf("renderBindPreparation(%q) error = %v", source, err)
		}
	}
	script, err := renderBindPreparation(domain.WorkloadSpec{
		Mounts: []domain.Mount{
			{Kind: domain.MountVolume, Target: bindPreparationTestTarget},
			{Kind: domain.MountBind, Source: recommendedLocaltimePath, Target: recommendedLocaltimePath, ReadOnly: true},
		},
	})
	if err != nil || script != nil {
		t.Fatalf("renderBindPreparation(non-writable mounts) = %q, %v", script, err)
	}
}

func TestExecuteBindPreparationTemplateReportsExecutionFailure(t *testing.T) {
	t.Parallel()

	broken := template.Must(template.New("broken").Parse("{{call .UID}}"))
	if _, err := executeBindPreparationTemplate(broken, bindPreparationTemplateData{UID: "1000"}); err == nil {
		t.Fatal("executeBindPreparationTemplate() succeeded")
	}
}

func TestBindPreparationScriptPreparesFilesAndDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	directory := filepath.Join(root, "existing")
	file := filepath.Join(root, "config")
	missing := filepath.Join(root, "new'data")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	workload := domain.WorkloadSpec{
		User: fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Mounts: []domain.Mount{
			{Kind: domain.MountBind, Source: directory, Target: "/directory"},
			{Kind: domain.MountBind, Source: file, Target: "/file"},
			{Kind: domain.MountBind, Source: missing, Target: "/missing"},
		},
	}
	runPreparationScript(t, workload, nil, false)
	for path, wantMode := range map[string]os.FileMode{
		directory: 0o750,
		file:      0o640,
		missing:   0o750,
	} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != wantMode {
			t.Fatalf("prepared %q = %#v, %v", path, info, err)
		}
	}
	if info, err := os.Stat(missing); err != nil || !info.IsDir() {
		t.Fatalf("missing bind source = %#v, %v", info, err)
	}
}

func TestBindPreparationScriptCreatesReviewedMissingFile(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "config", "service.conf")
	workload := domain.WorkloadSpec{
		User:   fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Mounts: []domain.Mount{{Kind: domain.MountBind, Source: file, Target: bindPreparationTestTarget}},
	}
	script, err := renderBindPreparation(workload)
	if err != nil {
		t.Fatalf("renderBindPreparation() error = %v", err)
	}
	script = bytes.Replace(script, []byte("prepare_bind directory "), []byte("prepare_bind file "), 1)
	runRenderedPreparationScript(t, script, nil, false)
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o640 {
		t.Fatalf("prepared file = %#v, %v", info, err)
	}
}

func TestBindPreparationScriptRejectsSymlink(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	link := filepath.Join(root, "link")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	workload := domain.WorkloadSpec{
		User:   fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()),
		Mounts: []domain.Mount{{Kind: domain.MountBind, Source: link, Target: bindPreparationTestTarget}},
	}
	runPreparationScript(t, workload, nil, true)
}

func runPreparationScript(t *testing.T, workload domain.WorkloadSpec, environment []string, wantFailure bool) {
	t.Helper()

	script, err := renderBindPreparation(workload)
	if err != nil {
		t.Fatalf("renderBindPreparation() error = %v", err)
	}
	runRenderedPreparationScript(t, script, environment, wantFailure)
}

func runRenderedPreparationScript(t *testing.T, script []byte, environment []string, wantFailure bool) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "prepare.sh")
	if err := os.WriteFile(path, script, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext( //nolint:gosec // The generated local script is the behavior under test.
		t.Context(), "sh", path,
	)
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if wantFailure != (err != nil) {
		t.Fatalf("sh prepare script = %v, %q", err, output)
	}
}
