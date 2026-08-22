package runtimeargv

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	testRelativePath      = "relative"
	testBindMountOption   = "--mount=type=bind,source=/a,target=/x"
	testTmpfsMountOption  = "--mount=type=tmpfs,target=/x"
	testDevicePath        = "/dev/a"
	testDeviceOption      = "--device=/dev/a:/dev/x"
	testSysctlOption      = "--sysctl=net.ipv4.ip_forward=1"
	testUlimitOption      = "--ulimit=nofile=1"
	testEnvironmentOption = "--env=A=1"
	testMissingValue      = "missing"
)

//nolint:cyclop // This test intentionally keeps the related value-boundary matrix in one audit surface.
func TestValueGrammarBoundaries(t *testing.T) {
	t.Parallel()

	invalidIntegers := []string{"", "+1", "x", "9223372036854775808", "11"}
	for _, value := range invalidIntegers {
		if _, err := canonicalBoundedInteger(value, 0, 10, false); !errors.Is(err, ErrInvalid) {
			t.Errorf("canonicalBoundedInteger(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"", "1x"} {
		if asciiDigits(value) {
			t.Errorf("asciiDigits(%q) = true", value)
		}
	}

	for input, want := range map[string]string{"1b": "1", "2k": "2048", "3g": "3221225472"} {
		got, err := canonicalByteSize(input, math.MaxInt64)
		if err != nil || got != want {
			t.Errorf("canonicalByteSize(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	for _, value := range []string{"01", "9223372036854775808", "9g"} {
		if _, err := canonicalByteSize(value, 8); !errors.Is(err, ErrInvalid) {
			t.Errorf("canonicalByteSize(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{
		"", "+1", "-1", "x", "1.x", ".", "1.000001",
	} {
		if _, err := canonicalCPU(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("canonicalCPU(%q) error = %v", value, err)
		}
	}
	if got, err := canonicalCPU(".5"); err != nil || got != "0.5" {
		t.Fatalf("canonicalCPU(.5) = %q, %v", got, err)
	}
	if got, rounded, err := composeCPU("1024.12345"); err != nil || !rounded || got != "1024.1234" {
		t.Fatalf("composeCPU() = %q, %t, %v", got, rounded, err)
	}
	if _, _, err := composeCPU(strings.Repeat("9", 100)); !errors.Is(err, ErrInvalid) {
		t.Fatalf("composeCPU(overflow) error = %v", err)
	}
	if optionalTrue(false) != nil || optionalTrue(true) == nil {
		t.Fatal("optionalTrue() did not normalize false")
	}
	for _, value := range []string{"bad\x00value", "é"} {
		if validSysctlValue(value) {
			t.Errorf("validSysctlValue(%q) = true", value)
		}
	}
	for _, value := range []string{
		"kernel.msgmax", "kernel.msgmnb", "kernel.msgmni", "kernel.sem", "kernel.shmall",
		"kernel.shmmax", "kernel.shmmni", "kernel.shm_rmid_forced", "fs.mqueue.msg_max", "net.ipv4.ip_forward",
	} {
		if !namespacedSysctl(value) {
			t.Errorf("namespacedSysctl(%q) rejected", value)
		}
	}
	if namespacedSysctl("kernel.hostname") {
		t.Fatal("namespacedSysctl(kernel.hostname) accepted")
	}
}

//nolint:cyclop,funlen // This test intentionally keeps the related storage-boundary matrix in one audit surface.
func TestStorageGrammarAndConflictBoundaries(t *testing.T) {
	t.Parallel()

	invalidVolumes := []string{testRelativePath, "/a:/b:/c:/d", ":/b", "/a:relative", "/a:/b:bad", "/a/../b:/c"}
	for _, value := range invalidVolumes {
		if _, err := parseVolume(value, testWorkingDirectory); !errors.Is(err, ErrInvalid) {
			t.Errorf("parseVolume(%q) error = %v", value, err)
		}
	}
	for _, mode := range []string{"", "ro,ro", "ro,rw", "bad"} {
		if validVolumeModes(mode) {
			t.Errorf("validVolumeModes(%q) = true", mode)
		}
	}

	invalidMounts := []string{
		"", "type=bind,source=/a,target=/b,target=/c", "type=bind,source=/a,target=/b,ro=maybe",
		"type=bind,source=/a,target=/b,extra=x", "type=bind,source=relative,target=/b", "type=volume,target=/b",
		"type=tmpfs,target=" + testRelativePath, "type=tmpfs,target=/b,tmpfs-size=0", "type=tmpfs,target=/b,tmpfs-mode=999",
	}
	for _, value := range invalidMounts {
		if _, _, err := parseMount(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("parseMount(%q) error = %v", value, err)
		}
	}
	if onlyMountKeys(map[string]string{"bad": "x"}, "type") {
		t.Fatal("onlyMountKeys accepted unknown key")
	}
	if onlyMountKeys(map[string]string{"a": "x", "b": "x"}, "a") {
		t.Fatal("onlyMountKeys accepted excess key")
	}

	invalidTmpfs := []string{
		testRelativePath, "/x:", "/x:ro,ro", "/x:ro,rw", "/x:exec,noexec", "/x:suid,nosuid",
		"/x:dev,nodev", "/x:ro=true", "/x:size", "/x:size=0", "/x:mode", "/x:mode=999", "/x:unknown",
	}
	for _, value := range invalidTmpfs {
		if _, err := parseTmpfs(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("parseTmpfs(%q) error = %v", value, err)
		}
	}
	for _, mode := range []string{"77", "000000", "999"} {
		if _, err := canonicalTmpfsMode(mode); !errors.Is(err, ErrInvalid) {
			t.Errorf("canonicalTmpfsMode(%q) error = %v", mode, err)
		}
	}
	if got, err := canonicalTmpfsMode("000"); err != nil || got != "0" {
		t.Fatalf("canonicalTmpfsMode(000) = %q, %v", got, err)
	}

	arguments := []string{dockerRuntime, createOperation,
		"--volume=/a:/same", "--volume=/a:/same",
		"--mount=type=bind,source=/b,target=/other", "--mount=type=bind,source=/b,target=/other",
		"--tmpfs=/tmp:ro", "--tmpfs=/tmp:ro", testImage}
	if _, err := Parse(arguments, "", testWorkingDirectory); err != nil {
		t.Fatalf("Parse(idempotent storage) error = %v", err)
	}
	conflicts := [][]string{
		{"--volume=/a:/x", "--volume=/b:/x"}, {testBindMountOption, "--device=/dev/a:/x"},
		{"--tmpfs=/x", "--volume=/a:/x"}, {"--device=/dev/a:/x", testTmpfsMountOption},
		{"--tmpfs=/x:ro", testTmpfsMountOption},
		{testTmpfsMountOption, "--mount=type=tmpfs,target=/x,ro"},
		{testBindMountOption, "--mount=type=bind,source=/b,target=/x"},
		{"--tmpfs=/x", testBindMountOption},
	}
	for _, options := range conflicts {
		argv := append([]string{dockerRuntime, createOperation}, options...)
		argv = append(argv, testImage)
		if _, err := Parse(argv, "", testWorkingDirectory); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(storage conflict %q) error = %v", options, err)
		}
	}
}

//nolint:cyclop,funlen,gocognit // This test keeps related option boundaries in one audit surface.
func TestPortDeviceHostSysctlAndUlimitBoundaries(t *testing.T) {
	t.Parallel()

	validPorts := map[string]containerconfig.PortBinding{
		"8080:80/udp":            {PublishedPort: 8080, TargetPort: 80, Protocol: "udp"},
		"[2001:0db8::1]:8080:80": {HostIP: "2001:db8::1", PublishedPort: 8080, TargetPort: 80, Protocol: "tcp"},
	}
	for input, want := range validPorts {
		got, err := parsePort(input)
		if err != nil || got != want {
			t.Errorf("parsePort(%q) = %#v, %v; want %#v", input, got, err, want)
		}
	}
	for _, value := range []string{
		"8080:80/sctp", "8080:80/tcp/x", "bad:8080:80", "[::1", "[::1]8080:80", "[::1]:80",
		"[::1]:8080:80:1", "80", "1:2:3:4", "x:80", "0:80", "65536:80",
	} {
		if _, err := parsePort(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("parsePort(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"8080:x", "8080:0"} {
		if _, err := parsePort(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("parsePort(target %q) error = %v", value, err)
		}
	}

	validDevices := map[string]containerconfig.DeviceMapping{
		testDevicePath:         {Source: testDevicePath, Target: testDevicePath, Permissions: "rwm"},
		testDevicePath + ":wm": {Source: testDevicePath, Target: testDevicePath, Permissions: "wm"},
		"/dev/a:/dev/b":        {Source: testDevicePath, Target: "/dev/b", Permissions: "rwm"},
		"/a:/b:rr":             {Source: "/a", Target: "/b", Permissions: "r"},
	}
	for input, want := range validDevices {
		got, err := parseDevice(input)
		if err != nil || got != want {
			t.Errorf("parseDevice(%q) = %#v, %v; want %#v", input, got, err, want)
		}
	}
	for _, value := range []string{"/a:/b:r:w", "relative", "/a:/b:x"} {
		if _, err := parseDevice(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("parseDevice(%q) error = %v", value, err)
		}
	}

	conflicts := [][]string{
		{testDeviceOption, "--device=/dev/b:/dev/x"},
		{testDeviceOption, "--volume=/host:/dev/x"},
		{"--add-host=a:1.1.1.1", "--add-host=a:2.2.2.2"},
		{testSysctlOption, "--sysctl=net.ipv4.ip_forward=0"},
		{testUlimitOption, "--ulimit=nofile=2"},
		{testEnvironmentOption, "--env=A=2"},
	}
	for _, options := range conflicts {
		argv := append([]string{dockerRuntime, createOperation}, options...)
		argv = append(argv, testImage)
		if _, err := Parse(argv, "", testWorkingDirectory); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(conflict %q) error = %v", options, err)
		}
	}
	for _, value := range []string{testMissingValue, "bad host=1.1.1.1", "host=[::1", "host=name"} {
		parser := newArgvParser(createOperation, []string{dockerRuntime, createOperation, testImage})
		if err := parser.addExtraHost(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("addExtraHost(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{testMissingValue, "bad=1", "net.x=é"} {
		parser := newArgvParser(createOperation, []string{dockerRuntime, createOperation, testImage})
		if err := parser.addSysctl(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("addSysctl(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"-1:2", "3:2", "1:bad"} {
		if _, err := parseUlimit("nofile", value); !errors.Is(err, ErrInvalid) {
			t.Errorf("parseUlimit(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{testMissingValue, "unknown=1", "nofile=bad"} {
		parser := newArgvParser(createOperation, []string{dockerRuntime, createOperation, testImage})
		if err := parser.addUlimits(value); !errors.Is(err, ErrInvalid) {
			t.Errorf("addUlimits(%q) error = %v", value, err)
		}
	}
}

//nolint:cyclop,funlen,gocognit,gocyclo // The related parser-boundary matrix forms one audit surface.
func TestHealthAndParserInternalFailureBoundaries(t *testing.T) {
	t.Parallel()

	parser := newArgvParser(createOperation, []string{dockerRuntime, createOperation, testImage})
	for _, field := range []string{
		healthCommand, healthInterval, healthRetries, healthStartInterval, healthStartPeriod, healthTimeout,
	} {
		value := "1s"
		switch field {
		case healthCommand:
			value = "true"
		case healthRetries:
			value = "1"
		}
		if err := parser.setHealth(field, value); err != nil {
			t.Fatalf("setHealth(%s) error = %v", field, err)
		}
		if err := parser.setHealth(field, value); err != nil {
			t.Fatalf("setHealth(%s duplicate) error = %v", field, err)
		}
	}
	if err := parser.setHealth(healthRetries, "2"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("setHealth(conflict) error = %v", err)
	}
	if err := parser.setHealth(healthCommand, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("setHealth(invalid command) error = %v", err)
	}
	invalidHealthValues := map[string]string{
		"unknown": "1", healthCommand: "", healthInterval: "1day", healthRetries: "-1",
	}
	for field, value := range invalidHealthValues {
		if _, err := canonicalHealthValue(field, value); !errors.Is(err, ErrInvalid) {
			t.Errorf("canonicalHealthValue(%q, %q) error = %v", field, value, err)
		}
	}

	for _, argv := range [][]string{
		{dockerRuntime, createOperation, "--no-healthcheck=maybe", testImage},
		{dockerRuntime, createOperation, "--health-cmd=", testImage},
		{dockerRuntime, createOperation, "--mount=", testImage},
		{dockerRuntime, createOperation, "--name=", testImage},
		{dockerRuntime, createOperation, "--env=", testImage},
		{dockerRuntime, createOperation, "--unknown", testImage},
		{dockerRuntime, createOperation, "-x", testImage},
	} {
		if _, err := Parse(argv, "", testWorkingDirectory); !errors.Is(err, ErrInvalid) {
			t.Errorf("Parse(%q) error = %v", argv, err)
		}
	}
	if shortBooleanCluster("") || shortBooleanCluster("ix") {
		t.Fatal("shortBooleanCluster accepted invalid cluster")
	}
	if got := oppositeAccess("unknown"); got != "" {
		t.Fatalf("oppositeAccess(unknown) = %q", got)
	}
	parser = newArgvParser(createOperation, []string{dockerRuntime, createOperation, testImage})
	if err := parser.addRepeated("unknown", "x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("addRepeated(unknown) error = %v", err)
	}
	if got := parser.absolutePath("/a/../b"); got != "/b" {
		t.Fatalf("absolutePath = %q", got)
	}
	if got := appendUnique([]string{"same"}, "same"); len(got) != 1 {
		t.Fatalf("appendUnique duplicate = %q", got)
	}
	if err := parser.addRepeated(repeatedDNSOption, "bad value"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("addRepeated(invalid DNS option) error = %v", err)
	}
	if err := parser.addRepeated(repeatedDNSSearch, "bad value"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("addRepeated(invalid DNS search) error = %v", err)
	}
	parser.setBoolean("unknown", true)
	for field, value := range map[string]string{
		nameField: "Bad Name", entrypointField: "bad\x00value", fieldCgroup: "unknown", fieldCgroupParent: "bad value",
	} {
		if _, err := canonicalScalar(field, value); !errors.Is(err, ErrInvalid) {
			t.Errorf("canonicalScalar(%q, %q) error = %v", field, value, err)
		}
	}
	if field, found := scalarOption("-u"); !found || field != fieldUser {
		t.Fatalf("scalarOption(-u) = %q, %t", field, found)
	}
	if field, found := scalarOption("--user"); !found || field != fieldUser {
		t.Fatalf("scalarOption(--user) = %q, %t", field, found)
	}
	if platformString(containerconfig.Platform{OS: linuxOS, Architecture: amd64Architecture}) != "linux/amd64" {
		t.Fatal("platformString omitted architecture")
	}
	if defaultServiceName("example.test/team/image") != "image" {
		t.Fatal("defaultServiceName without tag")
	}
	parser.service.ExtraHosts = []string{"first=127.0.0.1"}
	if err := parser.addExtraHost("second=127.0.0.2"); err != nil {
		t.Fatalf("addExtraHost(second) error = %v", err)
	}
	parser.service.Ports = nil
	if err := parser.addPort("127.0.0.1:8080:80"); err != nil {
		t.Fatal(err)
	}
	if err := parser.addPort("127.0.0.1:8080:80"); err != nil || len(parser.service.Ports) != 1 {
		t.Fatalf("addPort(duplicate) = %#v, %v", parser.service.Ports, err)
	}
	parser.service.Mounts = []containerconfig.Mount{{Target: "/dev/x"}}
	if err := parser.addDevice("/dev/a:/dev/x"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("addDevice(storage conflict) error = %v", err)
	}
	parser.service.Mounts = nil
	parser.service.Tmpfs = []containerconfig.TmpfsMount{{Target: "/tmp", Options: []string{"ro"}}}
	if err := parser.addTmpfs("/tmp:rw"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("addTmpfs(conflict) error = %v", err)
	}
}

func TestCollectionLimitsAndIdempotence(t *testing.T) {
	t.Parallel()

	parser := newArgvParser(createOperation, []string{dockerRuntime, createOperation, testImage})
	for index := range maximumSysctls {
		if err := parser.addSysctl("net.test.key" + strings.Repeat("x", index) + "=1"); err != nil {
			t.Fatalf("addSysctl(%d) error = %v", index, err)
		}
	}
	if err := parser.addSysctl("net.one.more=1"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("addSysctl(over limit) error = %v", err)
	}

	for _, options := range [][]string{
		{"--device=/dev/b", "--device=/dev/a"},
		{testDeviceOption, testDeviceOption},
		{"--add-host=a:1.1.1.1", "--add-host=a=1.1.1.1"},
		{testSysctlOption, testSysctlOption},
		{testUlimitOption, testUlimitOption},
		{testEnvironmentOption, testEnvironmentOption},
	} {
		argv := append([]string{dockerRuntime, createOperation}, options...)
		argv = append(argv, testImage)
		if _, err := Parse(argv, "", testWorkingDirectory); err != nil {
			t.Errorf("Parse(idempotent %q) error = %v", options, err)
		}
	}
}
