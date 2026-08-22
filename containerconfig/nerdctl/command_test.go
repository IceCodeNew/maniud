package nerdctl_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/containerconfig/nerdctl"
	"github.com/IceCodeNew/maniud/containerconfig/runtimeargv"
)

const (
	testWorkingDirectory = "/workspace/project"
	testImage            = "example.com/team/app:1"
	testService          = "service"
	changedValue         = "changed"
)

func TestCompleteCommandRoundTripsDeterministically(t *testing.T) {
	t.Parallel()

	arguments := []string{
		runtimeargv.RuntimeNerdctl, runtimeargv.OperationRun, "--detach", "--pull=never",
		"--blkio-weight=500", "--cgroup-parent=/team.slice", "--cgroupns=private", "--cpus=1.5",
		"--entrypoint=/init", "--hostname=app_1", "--memory=536870912", "--name=service",
		"--network=bridge", "--oom-score-adj=500", "--platform=linux/arm64/v8", "--pids-limit=-1",
		"--restart=on-failure:3", "--shm-size=67108864", "--stop-signal=SIGTERM", "--stop-timeout=30",
		"--user=1000:1000", "--workdir=/app", "--init=false", "--interactive", "--oom-kill-disable",
		"--read-only", "--tty", "--cap-add=NET_ADMIN", "--cap-drop=MKNOD", "--dns=2001:db8::1",
		"--dns-option=ndots:1", "--dns-search=svc.local", "--device=/dev/fuse:/dev/fuse:rw",
		"--device=/dev/null:/dev/null:rwm",
		"--add-host=cache:[2001:db8::2]", "--group-add=42", "--sysctl=net.ipv4.ip_forward=1",
		"--tmpfs=/cache:ro,noexec,size=2097152,mode=1777", "--ulimit=nofile=1024:2048,core=0:0",
		"--tmpfs=/other:rw", "--tmpfs=/plain", "--env=FOO=bar", "--env-file=.env",
		"--expose=443/tcp", "--expose=53/udp", "--label=team=platform",
		"--publish=[::1]:8080:80/tcp", "--publish=127.0.0.1:9090:90/udp", "--publish=7070:70/tcp",
		"--security-opt=no-new-privileges", "--volume=/anonymous",
		"--mount=type=bind,source=/host/data,target=/data,readonly",
		"--mount=type=bind,source=/host/cache,target=/host-cache",
		"--health-cmd=test -f /ready", "--health-interval=30s", "--health-timeout=2s",
		"--health-retries=3", "--health-start-period=1m30s", testImage, "serve", "--listen=:80",
	}
	command, err := nerdctl.Parse(arguments, testService, testWorkingDirectory)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	encoded, err := nerdctl.Encode(command)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	second, err := nerdctl.Encode(command)
	if err != nil || !slices.Equal(encoded, second) {
		t.Fatalf("second Encode() = %q, %v", second, err)
	}
	decoded, err := nerdctl.Parse(encoded, testService, "/")
	if err != nil {
		t.Fatalf("Parse(Encode()) error = %v\n%q", err, encoded)
	}
	roundTrip, err := nerdctl.Encode(decoded)
	if err != nil || !slices.Equal(roundTrip, encoded) {
		t.Fatalf("Encode(Parse(Encode())) = %q, %v", roundTrip, err)
	}
	if err := nerdctl.Validate(command); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	assertOwnedClone(t, decoded)
}

func assertOwnedClone(t *testing.T, decoded nerdctl.Command) {
	t.Helper()

	clone := decoded.Clone()
	clone.Spec.Command[0] = changedValue
	clone.EnvironmentFiles[0] = changedValue
	if decoded.Spec.Command[0] == changedValue || decoded.EnvironmentFiles[0] == changedValue {
		t.Fatal("Clone returned shared state")
	}
}

func TestMinimalAndDisabledHealthCommandsRoundTrip(t *testing.T) {
	t.Parallel()

	minimal, err := nerdctl.Parse([]string{
		runtimeargv.RuntimeNerdctl, runtimeargv.OperationCreate, testImage,
	}, "", testWorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := nerdctl.Encode(minimal); err != nil {
		t.Fatalf("Encode(minimal) error = %v", err)
	}

	disabled, err := nerdctl.Parse([]string{
		runtimeargv.RuntimeNerdctl, runtimeargv.OperationCreate, "--no-healthcheck",
		"--platform=linux/amd64", testImage,
	}, "", testWorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := nerdctl.Encode(disabled)
	if err != nil || !slices.Contains(encoded, "--no-healthcheck") {
		t.Fatalf("Encode(disabled health) = %q, %v", encoded, err)
	}
}

func TestEncodePreservesOrderedResolverAndEnvironmentFileOptions(t *testing.T) {
	t.Parallel()

	command, err := nerdctl.Parse([]string{
		runtimeargv.RuntimeNerdctl, runtimeargv.OperationCreate,
		"--dns=192.0.2.2", "--dns=192.0.2.1",
		"--dns-option=timeout:1", "--dns-option=rotate",
		"--dns-search=second.example", "--dns-search=first.example",
		"--env-file=second.env", "--env-file=first.env",
		testImage,
	}, "", testWorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := nerdctl.Encode(command)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	for _, ordered := range [][2]string{
		{"--dns=192.0.2.2", "--dns=192.0.2.1"},
		{"--dns-option=timeout:1", "--dns-option=rotate"},
		{"--dns-search=second.example", "--dns-search=first.example"},
		{"--env-file=" + testWorkingDirectory + "/second.env", "--env-file=" + testWorkingDirectory + "/first.env"},
	} {
		first := slices.Index(encoded, ordered[0])
		second := slices.Index(encoded, ordered[1])
		if first < 0 || second < 0 || first >= second {
			t.Fatalf("Encode() options = %q, want %q before %q", encoded, ordered[0], ordered[1])
		}
	}
}

func TestRejectsOtherClientsAndUnrepresentableConfiguration(t *testing.T) {
	t.Parallel()

	if _, err := nerdctl.Parse([]string{
		runtimeargv.RuntimeDocker, runtimeargv.OperationCreate, testImage,
	}, "", testWorkingDirectory); validationCode(err) != containerconfig.ValidationInvalidDocument {
		t.Fatalf("Parse(Docker) error = %v", err)
	}
	if _, err := nerdctl.Parse(nil, "", testWorkingDirectory); validationCode(err) !=
		containerconfig.ValidationInvalidDocument {
		t.Fatalf("Parse(nil) error = %v", err)
	}

	command, err := nerdctl.Parse([]string{
		runtimeargv.RuntimeNerdctl, runtimeargv.OperationCreate, "--health-cmd=true", testImage,
	}, "", testWorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	command.Spec.Healthcheck.StartInterval = "1s"
	if err := nerdctl.Validate(command); validationCode(err) != containerconfig.ValidationUnsupportedCapability ||
		!strings.Contains(err.Error(), "/healthcheck/start_interval") {
		t.Fatalf("Validate(start interval) error = %v", err)
	}
	command.Spec.Healthcheck.StartInterval = ""
	command.Spec.Entrypoint = []string{"/first", "/second"}
	if _, err := nerdctl.Encode(command); validationCode(err) != containerconfig.ValidationInvalidValue {
		t.Fatalf("Encode(multiple entrypoints) error = %v", err)
	}
	command.Spec.Entrypoint = nil
	command.Spec.Healthcheck.Test = []string{"CMD", "true"}
	if _, err := nerdctl.Encode(command); validationCode(err) != containerconfig.ValidationInvalidValue {
		t.Fatalf("Encode(exec healthcheck) error = %v", err)
	}
}

func validationCode(err error) containerconfig.ValidationCode {
	var validation containerconfig.ValidationError
	if !errors.As(err, &validation) {
		return ""
	}

	return validation.Code
}
