package runtimeargv

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
)

//nolint:cyclop,funlen,gocyclo // The assertions keep the complete supported argv grammar in one audit surface.
func TestParseProjectsCompleteSupportedGrammar(t *testing.T) {
	t.Parallel()

	arguments := []string{
		dockerRuntime, createOperation,
		"--blkio-weight=500", "--cgroup-parent=/team.slice", "--cgroupns=private", "--cpus=1.50000",
		"--entrypoint=/init", "--hostname=app_1", "-m512m", testNamedService, "--network=default",
		"--oom-score-adj=+0500", testARM64Option, "--pids-limit=-1", "--restart=on-failure:3",
		"--shm-size=64m", "--stop-signal=SIGTERM", "--stop-timeout=30", "-u1000:1000", "-w", "/app",
		"--init=false", "-i=false", "--oom-kill-disable", "--read-only=true", "-t",
		"--cap-add=cap_net_admin", "--cap-add=CHOWN", "--cap-drop=MKNOD", "--dns=2001:0db8::1",
		"--dns-option=ndots:1", "--dns-search=svc.local", "--device=/dev/fuse:/dev/fuse:wm",
		"--add-host=cache:[2001:db8::2]", "--group-add=00042", testSysctlOption,
		"--tmpfs=/cache:ro,noexec,nosuid,nodev,size=2m,mode=01777", "--ulimit=nofile=1024:2048",
		"-eFOO=bar", "--env=BARE", "--env-file=.env", "--expose=80", "--expose=80",
		"--expose=53/udp", "-lteam=platform", "-p127.0.0.1:8080:80/tcp",
		"--security-opt=no-new-privileges", "-v/host/data:/data:ro", "-v./state:/state",
		"--volume=/anonymous", "--mount=type=bind,src=/host/config,dst=/config,readonly",
		"--mount=type=tmpfs,target=/scratch,ro,tmpfs-size=4m,tmpfs-mode=0755",
		"--health-cmd=test -f /ready", "--health-interval=30s", "--health-timeout=2s",
		"--health-retries=03", "--health-start-period=1m30s", "--health-start-interval=500ms",
		"team/app:1", testServeCommand, "--listen=:80",
	}
	projection, err := Parse(arguments, testService, testWorkingDirectory)
	if err != nil {
		parser := newArgvParser(createOperation, arguments)
		parser.workingDir = testWorkingDirectory
		for parser.index < len(arguments) && strings.HasPrefix(arguments[parser.index], "-") {
			token := arguments[parser.index]
			if parseErr := parser.parseOption(token); parseErr != nil {
				t.Fatalf("parseOption(%q) error = %v", token, parseErr)
			}
		}
		t.Fatalf("Parse(complete grammar) error = %v", err)
	}

	service := projection.service
	if service.CPUs != "1.5" || service.MemoryBytes != 512*1024*1024 ||
		service.SharedMemoryBytes != 64*1024*1024 ||
		service.OOMScoreAdj == nil || *service.OOMScoreAdj != 500 || service.PidsLimit == nil || *service.PidsLimit != -1 ||
		service.StopTimeout == nil || *service.StopTimeout != 30 || service.Healthcheck == nil ||
		service.Healthcheck.Retries == nil ||
		*service.Healthcheck.Retries != 3 {
		t.Fatalf("Parse(complete grammar) scalar projection = %#v", service)
	}
	if service.Init == nil || *service.Init || service.StdinOpen != nil ||
		service.OOMKillDisable == nil || !*service.OOMKillDisable ||
		service.ReadOnly == nil || !*service.ReadOnly || service.TTY == nil || !*service.TTY {
		t.Fatalf("Parse(complete grammar) boolean projection = %#v", service)
	}
	if got := strings.Join(service.CapAdd, ","); got != "CHOWN,NET_ADMIN" {
		t.Fatalf("cap_add = %q", got)
	}
	if len(service.Tmpfs) != 2 || service.Tmpfs[0].Target != "/cache" || service.Tmpfs[1].Target != "/scratch" ||
		strings.Join(service.Tmpfs[0].Options, ",") != "ro,noexec,nosuid,nodev,size=2097152,mode=1777" ||
		strings.Join(service.Tmpfs[1].Options, ",") != "ro,size=4194304,mode=755" {
		t.Fatalf("tmpfs = %#v", service.Tmpfs)
	}
	if len(service.Mounts) != 4 || len(service.Ulimits) != 1 || len(service.Devices) != 1 ||
		len(service.Environment) != 2 || len(service.ExposedPorts) != 2 || len(service.Ports) != 1 ||
		!service.NoNewPrivileges ||
		!slices.Equal(projection.EnvironmentFiles(), []string{testWorkingDirectory + "/.env"}) {
		t.Fatalf("Parse(complete grammar) collection projection = %#v", service)
	}

	portable := projection.Spec()
	if portable.ServiceName != testService || portable.Ulimits[0] != (containerconfig.Ulimit{
		Name: "nofile", Soft: 1024, Hard: 2048,
	}) || portable.Healthcheck.StartInterval != "500ms" {
		t.Fatalf("Spec() = %#v", portable)
	}
	portable.Ulimits[0].Name = testChangedValue
	if projection.Spec().Ulimits[0].Name == testChangedValue {
		t.Fatal("Spec returned mutable state")
	}
}

func TestParsePreservesNonDockerRuntimeProvenance(t *testing.T) {
	t.Parallel()

	for _, runtimeName := range []string{podmanRuntime, nerdctlRuntime} {
		projection := requireProjection(t, []string{runtimeName, createOperation, testImage})
		if projection.Runtime() != runtimeName {
			t.Fatalf("Runtime(%s) = %q", runtimeName, projection.Runtime())
		}
	}
}

func TestParseNormalizesRuntimeDefaultZeroValues(t *testing.T) {
	t.Parallel()

	projection, err := Parse([]string{
		dockerRuntime, createOperation, "--oom-score-adj=0", "--interactive=false",
		"--oom-kill-disable=false", "--read-only=false", "--tty=false", testImage,
	}, "", testWorkingDirectory)
	if err != nil {
		t.Fatalf("Parse(zero values) error = %v", err)
	}
	service := projection.service
	if service.OOMScoreAdj != nil || service.StdinOpen != nil || service.OOMKillDisable != nil ||
		service.ReadOnly != nil || service.TTY != nil {
		t.Fatalf("Parse(zero values) = %#v", service)
	}
}

func TestParseProjectsNativeIntegerScalars(t *testing.T) {
	t.Parallel()

	projection := requireProjection(t, []string{
		dockerRuntime, createOperation, "--oom-score-adj=-500", "--blkio-weight=500", testImage,
	})
	if projection.service.OOMScoreAdj == nil {
		t.Fatal("OOMScoreAdj = nil, want -500")
	}
	if got := *projection.service.OOMScoreAdj; got != -500 {
		t.Fatalf("OOMScoreAdj = %d, want -500", got)
	}
	if projection.service.BlkioWeight == nil {
		t.Fatal("BlkioWeight = nil, want 500")
	}
	if got := *projection.service.BlkioWeight; got != 500 {
		t.Fatalf("BlkioWeight = %d, want 500", got)
	}
}

func TestParseWarnsWhenComposeRoundsCPUs(t *testing.T) {
	t.Parallel()

	projection, err := Parse([]string{
		dockerRuntime, createOperation, "--cpus=1024.12345", testImage,
	}, "", testWorkingDirectory)
	if err != nil {
		t.Fatalf("Parse(rounded cpus) error = %v", err)
	}
	warnings := projection.Warnings()
	if projection.service.CPUs != "1024.1234" || len(warnings) != 1 ||
		warnings[0].Code != "runtime_value_rounded" || warnings[0].Option != "--cpus" ||
		!strings.Contains(warnings[0].Reason, "1024.12345") || !strings.Contains(warnings[0].Reason, "1024.1234") {
		t.Fatalf("Parse(rounded cpus) = %q, %#v", projection.service.CPUs, warnings)
	}
}

func TestParseAcceptsSupportedSpellingVariants(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{dockerRuntime, createOperation, "-it", "-hhost", "-e", "A=1", "-l", "a=1", "-p", "8080:80", testImage},
		{dockerRuntime, createOperation, "-ti", "--interactive", "--tty=false", "--hostname", "host", testImage},
		{podmanRuntime, createOperation, "--ulimit=RLIMIT_NOFILE=1:2,CORE=3", testImage},
		{nerdctlRuntime, createOperation, "--ulimit=nofile=1:2,core=3", testImage},
		{dockerRuntime, createOperation, noHealthcheckOption, testImage},
		{dockerRuntime, createOperation, "--no-healthcheck=false", testImage},
		{dockerRuntime, createOperation, "--mount=type=bind,source=/one,target=/two,read_only=false", testImage},
		{dockerRuntime, createOperation, "--mount=type=tmpfs,destination=/two,readonly=false", testImage},
	}
	for _, arguments := range tests {
		if _, err := Parse(arguments, "", testWorkingDirectory); err != nil {
			t.Errorf("Parse(%q) error = %v", arguments, err)
		}
	}
}

func TestParseRejectsUnsupportedValues(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{dockerRuntime, createOperation, "--init=1", testImage},
		{dockerRuntime, createOperation, "-ix", testImage},
		{dockerRuntime, createOperation, "--restart=bad", testImage},
		{dockerRuntime, createOperation, "--workdir=relative", testImage},
		{dockerRuntime, createOperation, "--hostname=-bad", testImage},
		{dockerRuntime, createOperation, "--cpus=0", testImage},
		{dockerRuntime, createOperation, "--cpus=" + strings.Repeat("9", 100), testImage},
		{dockerRuntime, createOperation, "--memory=0", testImage},
		{dockerRuntime, createOperation, "--pids-limit=0", testImage},
		{dockerRuntime, createOperation, "--oom-score-adj=1001", testImage},
		{dockerRuntime, createOperation, "--blkio-weight=9", testImage},
		{dockerRuntime, createOperation, "--stop-timeout=0", testImage},
		{dockerRuntime, createOperation, "--cap-add=?", testImage},
		{dockerRuntime, createOperation, "--dns=name", testImage},
		{dockerRuntime, createOperation, "--dns-option=a b", testImage},
		{dockerRuntime, createOperation, "--device=relative", testImage},
		{dockerRuntime, createOperation, "--add-host=host:host-gateway", testImage},
		{dockerRuntime, createOperation, "--group-add=-1", testImage},
		{dockerRuntime, createOperation, "--sysctl=kernel.hostname=x", testImage},
		{dockerRuntime, createOperation, "--tmpfs=relative", testImage},
		{dockerRuntime, createOperation, "--ulimit=unknown=1", testImage},
		{dockerRuntime, createOperation, "--env=BAD NAME=1", testImage},
		{dockerRuntime, createOperation, "--label==bad", testImage},
		{dockerRuntime, createOperation, "--expose=0", testImage},
		{dockerRuntime, createOperation, "--expose=80/sctp", testImage},
		{dockerRuntime, createOperation, "--publish=80", testImage},
		{dockerRuntime, createOperation, "--security-opt=seccomp=unconfined", testImage},
		{dockerRuntime, createOperation, "--volume=named:/data", testImage},
		{dockerRuntime, createOperation, "--mount=type=volume,source=name,target=/data", testImage},
		{podmanRuntime, createOperation, "--health-start-interval=1s", testHealthCommand, testImage},
		{nerdctlRuntime, createOperation, "--health-start-interval=1s", testHealthCommand, testImage},
		{dockerRuntime, createOperation, "--health-timeout=1s", testImage},
		{dockerRuntime, createOperation, noHealthcheckOption, testHealthCommand, testImage},
	}
	for _, arguments := range tests {
		if _, err := Parse(arguments, "", testWorkingDirectory); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) error = %v", arguments, err)
		}
	}
}

func requireProjection(t *testing.T, arguments []string) Projection {
	t.Helper()

	projection, err := Parse(arguments, "", testWorkingDirectory)
	if err != nil {
		t.Fatalf("Parse(%q) error = %v", arguments, err)
	}

	return projection
}
