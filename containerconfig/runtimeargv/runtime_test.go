package runtimeargv

import (
	"errors"
	"runtime"
	"slices"
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/imageref"
)

const (
	testImage            = "image:1"
	testWorkingDirectory = "/workspace/project"
	testService          = "service"
	testNamedService     = "--name=service"
	testARM64Option      = "--platform=linux/arm64"
	testServeCommand     = "serve"
	testInitEntrypoint   = "/init"
	testHealthCommand    = "--health-cmd=true"
	testExecOperation    = execOptionValue
	testChangedValue     = "changed"
)

func TestParseProjectsExecutableRuntimeSubset(t *testing.T) {
	t.Parallel()

	for _, runtimeName := range []string{dockerRuntime, podmanRuntime, nerdctlRuntime} {
		projection, err := Parse([]string{
			runtimeName, runOperation, detachOption, "--quiet=false", "--pull=never",
			testNamedService, "--network", "default", testARM64Option,
			"--entrypoint", testInitEntrypoint, "team/app:1", testServeCommand, "--flag",
		}, testService, testWorkingDirectory)
		if err != nil {
			t.Fatalf("Parse(%s) error = %v", runtimeName, err)
		}
		assertParsedProjection(t, runtimeName, projection)
		warnings := projection.Warnings()
		warnings[0].Option = testChangedValue
		if projection.Warnings()[0].Option == testChangedValue {
			t.Fatal("Warnings returned mutable state")
		}
	}
}

func assertParsedProjection(t *testing.T, runtimeName string, projection Projection) {
	t.Helper()

	if projection.Name() != testService || projection.Source().String() != "docker.io/team/app:1" ||
		projection.Operation() != runOperation ||
		projection.Platform() != (containerconfig.Platform{
			OS: linuxOS, Architecture: arm64Architecture, Variant: arm64Variant,
		}) ||
		!slices.Equal(projection.service.Entrypoint, []string{testInitEntrypoint}) ||
		!slices.Equal(projection.service.Command, []string{testServeCommand, "--flag"}) ||
		len(projection.Warnings()) != 3 {
		t.Fatalf("Parse(%s) = %#v", runtimeName, projection)
	}
}

func TestParseAcceptsSafeExecutionOnlyOptions(t *testing.T) {
	t.Parallel()

	projection, err := Parse([]string{
		dockerRuntime, runOperation, "-d=true", "-q", "--rm=false", "--sig-proxy=true",
		"--attach=stdout", "-a", "stderr", "--detach-keys", "ctrl-x", "--", testImage,
	}, "", testWorkingDirectory)
	if err != nil || len(projection.Warnings()) != 6 {
		t.Fatalf("Parse(ignored options) = %#v, %v", projection, err)
	}
}

func TestSpecCanonicalizesProcessAndHealthDefaults(t *testing.T) {
	t.Parallel()

	projection, err := Parse([]string{
		dockerRuntime, createOperation, "--entrypoint=/debug", testHealthCommand,
		"--health-retries=0", testImage,
	}, testService, testWorkingDirectory)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	portable := projection.Spec()
	if !slices.Equal(portable.Entrypoint, []string{"/debug"}) || portable.Command == nil ||
		len(portable.Command) != 0 || portable.Healthcheck == nil || portable.Healthcheck.Retries != nil {
		t.Fatalf("Spec() = %#v", portable)
	}
}

func TestParseSource(t *testing.T) {
	t.Parallel()

	projection, err := ParseSource("team/my_app:1", "")
	if err != nil {
		t.Fatalf("ParseSource() error = %v", err)
	}
	if projection.Name() != "my-app" || projection.Platform().OS != linuxOS ||
		projection.Platform().Architecture != runtime.GOARCH {
		t.Fatalf("ParseSource() = %#v", projection)
	}
	portable := projection.Spec()
	if portable.ServiceName != "my-app" || portable.Platform != projection.Platform() {
		t.Fatalf("Spec() = %#v", portable)
	}
}

func TestParseRejectsUnsupportedOrLossyArguments(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		nil,
		{"crictl", runOperation, testImage},
		{dockerRuntime, testExecOperation, testImage},
		{dockerRuntime, runOperation, "-", testImage},
		{dockerRuntime, runOperation, "--name"},
		{dockerRuntime, runOperation, "--name=", testImage},
		{dockerRuntime, runOperation, "--network=host", testImage},
		{dockerRuntime, runOperation, "--platform=windows/amd64", testImage},
		{dockerRuntime, runOperation, "--entrypoint=", testImage},
		{dockerRuntime, runOperation, "--detach=maybe", testImage},
		{dockerRuntime, createOperation, detachOption, testImage},
		{dockerRuntime, createOperation, "--attach=stdout", testImage},
		{dockerRuntime, runOperation, "--pull=sometimes", testImage},
		{dockerRuntime, runOperation, "--attach=logs", testImage},
		{dockerRuntime, runOperation, "--detach-keys=", testImage},
		{dockerRuntime, runOperation},
		{dockerRuntime, runOperation, "bad@@reference"},
		{dockerRuntime, runOperation, testImage, "bad\x00command"},
	}
	for _, arguments := range tests {
		if _, err := Parse(arguments, "", testWorkingDirectory); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) error = %v", arguments, err)
		}
	}
	if _, err := Parse([]string{dockerRuntime, runOperation, testImage}, "", "relative"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse(relative cwd) error = %v", err)
	}
}

func TestParseRejectsConflictsAndAcceptsCanonicalDuplicates(t *testing.T) {
	t.Parallel()

	valid, err := Parse([]string{
		dockerRuntime, createOperation, "--network=default", "--network=bridge",
		testARM64Option, "--platform=linux/arm64/v8", testNamedService, testNamedService, testImage,
	}, testService, testWorkingDirectory)
	if err != nil || valid.Name() != testService {
		t.Fatalf("Parse(canonical duplicates) = %#v, %v", valid, err)
	}
	runtimeNamed, err := Parse(
		[]string{dockerRuntime, createOperation, "--name=runtime-name", testImage},
		"",
		testWorkingDirectory,
	)
	if err != nil || runtimeNamed.Name() != "runtime-name" {
		t.Fatalf("Parse(runtime name) = %#v, %v", runtimeNamed, err)
	}

	for _, arguments := range [][]string{
		{dockerRuntime, runOperation, "--name=one", "--name=two", testImage},
		{dockerRuntime, runOperation, "--entrypoint=/one", "--entrypoint=/two", testImage},
	} {
		if _, err = Parse(arguments, "", testWorkingDirectory); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Parse(conflict %q) error = %v", arguments, err)
		}
	}
	_, err = Parse(
		[]string{dockerRuntime, runOperation, "--name=one", testImage},
		"two",
		testWorkingDirectory,
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse(conflicting explicit name) error = %v", err)
	}
}

func TestPlatformAndInternalValidationBoundaries(t *testing.T) {
	t.Parallel()

	for _, architecture := range []string{"386", ""} {
		if _, err := parseSourceForArchitecture(testImage, "", architecture); !errors.Is(err, ErrInvalid) {
			t.Fatalf("parseSourceForArchitecture(%q) error = %v", architecture, err)
		}
	}
	amd64Projection, err := parseSourceForArchitecture(testImage, "", amd64Architecture)
	if err != nil || amd64Projection.Platform() != (containerconfig.Platform{
		OS: linuxOS, Architecture: amd64Architecture,
	}) {
		t.Fatalf("parseSourceForArchitecture(amd64) = %#v, %v", amd64Projection, err)
	}
	arm64Projection, err := parseSourceForArchitecture(testImage, "", arm64Architecture)
	if err != nil || arm64Projection.Platform().Variant != arm64Variant {
		t.Fatalf("parseSourceForArchitecture(arm64) = %#v, %v", arm64Projection, err)
	}

	testInternalValidationBoundaries(t)
}

func testInternalValidationBoundaries(t *testing.T) {
	t.Helper()

	_, err := Parse(
		[]string{dockerRuntime, runOperation, "--platform=amd64", testImage},
		"",
		testWorkingDirectory,
	)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse(malformed platform) error = %v", err)
	}
	if _, err := ParseSource("bad@@reference", ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseSource(invalid) error = %v", err)
	}
	if _, err := ParseSource(testImage, "Bad Name"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("ParseSource(invalid name) error = %v", err)
	}
	if _, err := canonicalScalar("unknown", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("canonicalScalar(unknown) error = %v", err)
	}
	parser := newArgvParser(runOperation, []string{dockerRuntime, runOperation, testImage})
	if err := parser.setScalar("unknown", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("setScalar(unknown) error = %v", err)
	}
	parser.seenScalars[platformField] = "invalid"
	parser.service.ContainerName = testService
	if _, err := parser.finish("", imageref.Source{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("finish(invalid platform) error = %v", err)
	}
}
