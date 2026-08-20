package runtimeargv

import (
	"errors"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
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
		if projection.Name() != testService || projection.Source().String() != "docker.io/team/app:1" ||
			projection.Platform() != (domain.Platform{
				OS: linuxOS, Architecture: arm64Architecture, Variant: arm64Variant,
			}) ||
			!slices.Equal(projection.service.Entrypoint, []string{testInitEntrypoint}) ||
			!slices.Equal(projection.service.Command, []string{testServeCommand, "--flag"}) ||
			len(projection.Warnings()) != 3 {
			t.Fatalf("Parse(%s) = %#v", runtimeName, projection)
		}
		warnings := projection.Warnings()
		warnings[0].Option = "changed"
		if projection.Warnings()[0].Option == "changed" {
			t.Fatal("Warnings returned mutable state")
		}
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

func TestWorkloadCanonicalizesInheritedProcessAndHealthDefaults(t *testing.T) {
	t.Parallel()

	projection, err := Parse([]string{
		dockerRuntime, createOperation, "--entrypoint=/debug", testHealthCommand,
		"--health-retries=0", testImage,
	}, testService, testWorkingDirectory)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	digest := domain.Hash([]byte("runtime defaults"))
	reference, err := projection.Source().Pin(digest)
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}
	workload, err := projection.Workload(domain.ImageIdentity{
		Origin: domain.ImageOriginRegistry, Reference: reference.String(), ReferenceDigest: digest,
		Platform: projection.Platform(), Entrypoint: []string{testInitEntrypoint}, Command: []string{"serve"},
	})
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}
	if !slices.Equal(workload.Entrypoint, []string{"/debug"}) || workload.Command == nil ||
		len(workload.Command) != 0 || workload.Healthcheck == nil || workload.Healthcheck.Retries != nil {
		t.Fatalf("Workload() = %#v", workload)
	}
}

//nolint:cyclop // One test keeps source projection and every rejected identity variant adjacent.
func TestParseSourceAndWorkload(t *testing.T) {
	t.Parallel()

	projection, err := ParseSource("team/my_app:1", "")
	if err != nil {
		t.Fatalf("ParseSource() error = %v", err)
	}
	if projection.Name() != "my-app" || projection.Platform().OS != linuxOS ||
		projection.Platform().Architecture != runtime.GOARCH {
		t.Fatalf("ParseSource() = %#v", projection)
	}
	digest, err := domain.ParseDigest("sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.ImageIdentity{
		Origin:          domain.ImageOriginRegistry,
		Reference:       "docker.io/team/my_app:1@" + digest.String(),
		ReferenceDigest: digest, Platform: projection.Platform(),
	}
	workload, err := projection.Workload(identity)
	if err != nil || workload.ServiceName != "my-app" || workload.Platform != identity.Platform {
		t.Fatalf("Workload() = %#v, %v", workload, err)
	}
	mismatchedPlatform := domain.Platform{OS: linuxOS, Architecture: amd64Architecture}
	if identity.Platform.Architecture == amd64Architecture {
		mismatchedPlatform = domain.Platform{
			OS: linuxOS, Architecture: arm64Architecture, Variant: arm64Variant,
		}
	}

	invalid := []domain.ImageIdentity{
		{},
		{Origin: domain.ImageOriginDockerArchive, Reference: identity.Reference,
			ReferenceDigest: digest, Platform: identity.Platform},
		{Origin: domain.ImageOriginRegistry, Reference: identity.Reference,
			ReferenceDigest: digest, Platform: mismatchedPlatform},
		{Origin: domain.ImageOriginRegistry, Reference: "docker.io/team/other:1@" + digest.String(),
			ReferenceDigest: digest, Platform: identity.Platform},
	}
	for _, value := range invalid {
		if _, err = projection.Workload(value); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Workload(%#v) error = %v", value, err)
		}
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
