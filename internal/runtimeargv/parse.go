package runtimeargv

import (
	"path/filepath"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

const (
	fieldBlkio        = "blkio"
	fieldCgroup       = "cgroup"
	fieldCgroupParent = "cgroup_parent"
	fieldCPUs         = "cpus"
	fieldHostname     = "hostname"
	fieldMemory       = "memory"
	fieldOOMScore     = "oom_score"
	fieldPids         = "pids"
	fieldRestart      = "restart"
	fieldShm          = "shm"
	fieldStopSignal   = "stop_signal"
	fieldStopTimeout  = "stop_timeout"
	fieldUser         = "user"
	fieldWorkdir      = "workdir"
)

type argvParser struct {
	runtime        string
	operation      string
	workingDir     string
	arguments      []string
	index          int
	service        domain.WorkloadSpec
	seenScalars    map[string]string
	seenWarnings   map[string]bool
	warnings       []Warning
	envFiles       []string
	health         domain.Healthcheck
	healthFields   bool
	healthDisabled bool
}

func newArgvParser(operation string, arguments []string) argvParser {
	return argvParser{
		runtime:      arguments[0],
		operation:    operation,
		arguments:    arguments,
		index:        runtimePrefixLength,
		service:      domain.WorkloadSpec{NetworkMode: bridgeNetwork},
		seenScalars:  make(map[string]string),
		seenWarnings: make(map[string]bool),
	}
}

func (parser *argvParser) parseOptions() error {
	for parser.index < len(parser.arguments) && strings.HasPrefix(parser.arguments[parser.index], "-") {
		if parser.arguments[parser.index] == "--" {
			parser.index++

			return nil
		}
		if err := parser.parseOption(parser.arguments[parser.index]); err != nil {
			return err
		}
	}

	return nil
}

//nolint:cyclop // Dispatch keeps supported runtime option families explicit and fail-closed.
func (parser *argvParser) parseOption(token string) error {
	if token == "-" {
		return ErrInvalid
	}
	if handled, err := parser.parseShortToken(token); handled {
		return err
	}

	name, inlineValue, inline := strings.Cut(token, "=")
	if field, found := booleanOption(name); found {
		value, err := optionalBoolean(inlineValue, inline)
		if err != nil {
			return err
		}
		parser.index++
		parser.setBoolean(field, value)

		return nil
	}
	if name == noHealthcheckOption {
		value, err := optionalBoolean(inlineValue, inline)
		if err != nil {
			return err
		}
		parser.index++
		parser.healthDisabled = value

		return nil
	}
	if field, found := healthOption(name); found {
		if field == healthStartInterval && parser.runtime != dockerRuntime {
			return ErrInvalid
		}
		value, err := parser.requiredValue(inlineValue, inline)
		if err != nil {
			return err
		}

		return parser.setHealth(field, value)
	}
	if handled, err := parser.parseIgnored(name, inlineValue, inline); handled {
		return err
	}
	if name == "--mount" {
		value, err := parser.requiredValue(inlineValue, inline)
		if err != nil {
			return err
		}

		return parser.addMount(value)
	}

	return parser.parseDesired(name, inlineValue, inline)
}

func (parser *argvParser) parseShortToken(token string) (bool, error) {
	if strings.HasPrefix(token, "--") || len(token) <= 2 {
		return false, nil
	}
	if shortBooleanCluster(token[1:]) {
		for _, name := range token[1:] {
			if name == 'i' {
				parser.setBoolean(booleanInteractive, true)
			} else {
				parser.setBoolean(booleanTTY, true)
			}
		}
		parser.index++

		return true, nil
	}

	name := token[:2]
	value := strings.TrimPrefix(token[2:], "=")
	if _, found := scalarOption(name); found {
		return true, parser.parseDesired(name, value, true)
	}
	if _, found := repeatedOption(name); found {
		return true, parser.parseDesired(name, value, true)
	}

	return false, nil
}

func shortBooleanCluster(value string) bool {
	if value == "" {
		return false
	}
	for _, name := range value {
		if name != 'i' && name != 't' {
			return false
		}
	}

	return true
}

func (parser *argvParser) parseDesired(name, inlineValue string, inline bool) error {
	if field, found := scalarOption(name); found {
		value, err := parser.requiredValue(inlineValue, inline)
		if err != nil {
			return err
		}

		return parser.setScalar(field, value)
	}
	if field, found := repeatedOption(name); found {
		value, err := parser.requiredValue(inlineValue, inline)
		if err != nil {
			return err
		}

		return parser.addRepeated(field, value)
	}

	return ErrInvalid
}

//nolint:cyclop // This switch is the audited runtime-option-to-domain-field table.
func scalarOption(name string) (string, bool) {
	switch name {
	case "--blkio-weight":
		return fieldBlkio, true
	case "--cgroup-parent":
		return fieldCgroupParent, true
	case "--cgroupns":
		return fieldCgroup, true
	case "--cpus":
		return fieldCPUs, true
	case "--entrypoint":
		return entrypointField, true
	case "--hostname", "-h":
		return fieldHostname, true
	case "--memory", "-m":
		return fieldMemory, true
	case "--name":
		return nameField, true
	case "--network":
		return networkField, true
	case "--oom-score-adj":
		return fieldOOMScore, true
	case "--platform":
		return platformField, true
	case "--pids-limit":
		return fieldPids, true
	case "--restart":
		return fieldRestart, true
	case "--shm-size":
		return fieldShm, true
	case "--stop-signal":
		return fieldStopSignal, true
	case "--stop-timeout":
		return fieldStopTimeout, true
	case "--user", "-u":
		return fieldUser, true
	case "--workdir", "-w":
		return fieldWorkdir, true
	default:
		return "", false
	}
}

//nolint:cyclop // This switch is the audited repeated-option-to-domain-field table.
func repeatedOption(name string) (string, bool) {
	switch name {
	case "--cap-add":
		return repeatedCapAdd, true
	case "--cap-drop":
		return repeatedCapDrop, true
	case "--dns":
		return repeatedDNS, true
	case "--dns-option":
		return repeatedDNSOption, true
	case "--dns-search":
		return repeatedDNSSearch, true
	case "--device":
		return repeatedDevice, true
	case "--add-host":
		return repeatedExtraHost, true
	case "--group-add":
		return repeatedGroup, true
	case "--sysctl":
		return repeatedSysctl, true
	case "--tmpfs":
		return repeatedTmpfs, true
	case "--ulimit":
		return repeatedUlimit, true
	case "--env", "-e":
		return repeatedEnvironment, true
	case "--env-file":
		return repeatedEnvFile, true
	case "--label", "-l":
		return repeatedLabel, true
	case "--publish", "-p":
		return repeatedPort, true
	case "--security-opt":
		return repeatedSecurity, true
	case "--volume", "-v":
		return repeatedVolume, true
	default:
		return "", false
	}
}

func (parser *argvParser) requiredValue(inlineValue string, inline bool) (string, error) {
	if inline {
		parser.index++
		if !validText(inlineValue) {
			return "", ErrInvalid
		}

		return inlineValue, nil
	}
	if parser.index+1 >= len(parser.arguments) || !validText(parser.arguments[parser.index+1]) {
		return "", ErrInvalid
	}
	value := parser.arguments[parser.index+1]
	parser.index += 2

	return value, nil
}

func optionalBoolean(value string, inline bool) (bool, error) {
	if !inline || value == booleanTrue {
		return true, nil
	}
	if value == "false" {
		return false, nil
	}

	return false, ErrInvalid
}

func (parser *argvParser) setBoolean(field string, value bool) {
	switch field {
	case booleanInit:
		parser.service.Init = &value
	case booleanInteractive:
		parser.service.StdinOpen = optionalTrue(value)
	case booleanOOMKill:
		parser.service.OOMKillDisable = optionalTrue(value)
	case booleanReadOnly:
		parser.service.ReadOnly = optionalTrue(value)
	case booleanTTY:
		parser.service.TTY = optionalTrue(value)
	}
}

func optionalTrue(value bool) *bool {
	if !value {
		return nil
	}

	return &value
}

func booleanOption(name string) (string, bool) {
	switch name {
	case "--init":
		return booleanInit, true
	case "--interactive", "-i":
		return booleanInteractive, true
	case "--oom-kill-disable":
		return booleanOOMKill, true
	case "--read-only":
		return booleanReadOnly, true
	case "--tty", "-t":
		return booleanTTY, true
	default:
		return "", false
	}
}

func (parser *argvParser) setScalar(field, value string) error {
	canonical, err := canonicalScalar(field, value)
	if err != nil {
		return err
	}
	if field == fieldCPUs {
		effective, rounded, roundErr := composeCPU(canonical)
		if roundErr != nil {
			return roundErr
		}
		if rounded {
			parser.addTypedWarning(
				"runtime_value_rounded",
				"--cpus",
				"Compose stores cpus as float32; requested "+canonical+" was rounded to "+effective,
			)
		}
		canonical = effective
	}
	if previous, found := parser.seenScalars[field]; found {
		if previous != canonical {
			return ErrInvalid
		}

		return nil
	}
	parser.seenScalars[field] = canonical
	parser.assignScalar(field, canonical)

	return nil
}

//nolint:cyclop // Assignment mirrors the scalar option table without hidden reflection.
func (parser *argvParser) assignScalar(field, value string) {
	switch field {
	case nameField:
		parser.service.ContainerName = value
	case networkField:
		parser.service.NetworkMode = value
	case entrypointField:
		parser.service.Entrypoint = []string{value}
	case fieldHostname:
		parser.service.Hostname = value
	case fieldUser:
		parser.service.User = value
	case fieldWorkdir:
		parser.service.WorkingDirectory = value
	case fieldRestart:
		parser.service.Restart = value
	case fieldStopSignal:
		parser.service.StopSignal = value
	case fieldCgroup:
		parser.service.Cgroup = value
	case fieldCgroupParent:
		parser.service.CgroupParent = value
	case fieldCPUs:
		parser.service.CPUs = value
	case fieldMemory:
		parser.service.MemoryBytes = canonicalInt64(value)
	case fieldShm:
		parser.service.SharedMemoryBytes = canonicalInt64(value)
	case fieldPids:
		parsed := canonicalInt64(value)
		parser.service.PidsLimit = &parsed
	case fieldOOMScore:
		parsed := int(canonicalInt64(value))
		if parsed != 0 {
			parser.service.OOMScoreAdj = &parsed
		}
	case fieldBlkio:
		weight := int(canonicalInt64(value))
		parser.service.BlkioWeight = &weight
	case fieldStopTimeout:
		seconds := canonicalInt64(value)
		parser.service.StopTimeout = &seconds
	}
}

//nolint:cyclop,funlen // Each scalar field has a distinct canonical grammar and bound.
func canonicalScalar(field, value string) (string, error) {
	switch field {
	case nameField:
		if validServiceName(value) {
			return value, nil
		}
	case networkField:
		if value == bridgeNetwork || value == "default" {
			return bridgeNetwork, nil
		}
	case platformField:
		platform, err := selectedPlatform(value)
		if err == nil {
			return platformString(platform), nil
		}
	case entrypointField, fieldUser, fieldStopSignal:
		if validText(value) {
			return value, nil
		}
	case fieldHostname:
		if validHostname(value) {
			return value, nil
		}
	case fieldWorkdir:
		if validContainerPath(value) {
			return value, nil
		}
	case fieldRestart:
		if validRestartPolicy(value) {
			return value, nil
		}
	case fieldCgroup:
		if value == "host" || value == "private" {
			return value, nil
		}
	case fieldCgroupParent:
		if validCgroupParent(value) {
			return value, nil
		}
	case fieldCPUs:
		return canonicalCPU(value)
	case fieldMemory:
		return canonicalByteSize(value, maximumMemoryBytes)
	case fieldShm:
		return canonicalByteSize(value, maximumSignedInteger)
	case fieldPids:
		if value == "-1" {
			return value, nil
		}

		return canonicalBoundedInteger(value, 1, maximumSignedInteger, false)
	case fieldOOMScore:
		return canonicalBoundedInteger(value, -maximumOOMScore, maximumOOMScore, true)
	case fieldBlkio:
		return canonicalBoundedInteger(value, minimumBlkioWeight, maximumBlkioWeight, false)
	case fieldStopTimeout:
		return canonicalBoundedInteger(value, 1, maximumSignedInteger, false)
	}

	return "", ErrInvalid
}

func canonicalInt64(value string) int64 {
	parsed, _ := strconv.ParseInt(value, 10, 64)

	return parsed
}

func (parser *argvParser) parseIgnored(name, value string, inline bool) (bool, error) {
	switch name {
	case "--detach", "-d":
		return true, parser.ignoreBoolean("--detach", executionReason, value, inline, true)
	case "--quiet", "-q":
		return true, parser.ignoreBoolean("--quiet", outputReason, value, inline, false)
	case "--rm":
		return true, parser.ignoreBoolean("--rm", executionReason, value, inline, true)
	case "--sig-proxy":
		return true, parser.ignoreBoolean("--sig-proxy", executionReason, value, inline, true)
	case "--pull":
		return true, parser.ignoreValue("--pull", pullReason, value, inline, validPullPolicy, false)
	case "--attach", "-a":
		return true, parser.ignoreValue("--attach", executionReason, value, inline, validAttach, true)
	case "--detach-keys":
		return true, parser.ignoreValue("--detach-keys", executionReason, value, inline, validText, true)
	default:
		return false, nil
	}
}

func (parser *argvParser) ignoreBoolean(
	canonical string,
	reason string,
	value string,
	inline bool,
	runOnly bool,
) error {
	if runOnly && parser.operation != runOperation {
		return ErrInvalid
	}
	if _, err := optionalBoolean(value, inline); err != nil {
		return err
	}
	parser.index++
	parser.addWarning(canonical, reason)

	return nil
}

func (parser *argvParser) ignoreValue(
	canonical string,
	reason string,
	value string,
	inline bool,
	validate func(string) bool,
	runOnly bool,
) error {
	if runOnly && parser.operation != runOperation {
		return ErrInvalid
	}
	selected, err := parser.requiredValue(value, inline)
	if err != nil || !validate(selected) {
		return ErrInvalid
	}
	parser.addWarning(canonical, reason)

	return nil
}

func validPullPolicy(value string) bool {
	return value == "always" || value == pullPolicyMissing || value == "never"
}

func validAttach(value string) bool {
	return value == "stdin" || value == "stdout" || value == "stderr"
}

func (parser *argvParser) addWarning(option, reason string) {
	parser.addTypedWarning("runtime_option_ignored", option, reason)
}

func (parser *argvParser) addTypedWarning(code, option, reason string) {
	if parser.seenWarnings[option] {
		return
	}
	parser.seenWarnings[option] = true
	parser.warnings = append(parser.warnings, Warning{Code: code, Option: option, Reason: reason})
}

func (parser *argvParser) setCommand(arguments []string) bool {
	for _, value := range arguments {
		if !validArgument(value) {
			return false
		}
	}
	parser.service.Command = append([]string(nil), arguments...)

	return true
}

func (parser *argvParser) finish(explicitName string, source imageref.Source) (Projection, error) {
	name := selectedServiceName(explicitName, parser.service.ContainerName, source.String())
	if !validSelectedName(explicitName, parser.service.ContainerName, name) {
		return Projection{}, ErrInvalid
	}
	platform, err := selectedPlatform(parser.seenScalars[platformField])
	if err != nil || !parser.finishHealthcheck() {
		return Projection{}, ErrInvalid
	}
	parser.service.ServiceName = name
	parser.service.ContainerName = name
	parser.service.Platform = platform
	if parser.service.Entrypoint != nil && parser.service.Command == nil {
		parser.service.Command = []string{}
	}

	return Projection{
		service: parser.service.Clone(), source: source, platform: platform,
		warnings: append([]Warning(nil), parser.warnings...), envFiles: append([]string(nil), parser.envFiles...),
		runtime: parser.runtime,
	}, nil
}

func (parser *argvParser) finishHealthcheck() bool {
	if parser.healthDisabled {
		if parser.healthFields {
			return false
		}
		parser.health.Disabled = true
		parser.service.Healthcheck = &parser.health

		return true
	}
	if !parser.healthFields {
		return true
	}
	if len(parser.health.Test) == 0 {
		return false
	}
	parser.service.Healthcheck = &parser.health

	return true
}

func (parser *argvParser) absolutePath(value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}

	return filepath.Join(parser.workingDir, value)
}
