package runtimeargv

// Internal field names keep client spellings out of the portable Spec.
const (
	booleanInit        = "init"
	booleanInteractive = "interactive"
	booleanOOMKill     = "oom_kill"
	booleanReadOnly    = "read_only"
	booleanTTY         = "tty"
	fieldBlkio         = "blkio"
	fieldCgroup        = "cgroup"
	fieldCgroupParent  = "cgroup_parent"
	fieldCPUs          = "cpus"
	fieldHostname      = "hostname"
	fieldMemory        = "memory"
	fieldOOMScore      = "oom_score"
	fieldPids          = "pids"
	fieldRestart       = "restart"
	fieldShm           = "shm"
	fieldStopSignal    = "stop_signal"
	fieldStopTimeout   = "stop_timeout"
	fieldUser          = "user"
	fieldWorkdir       = "workdir"

	repeatedCapAdd      = "cap_add"
	repeatedCapDrop     = "cap_drop"
	repeatedDNS         = "dns"
	repeatedDNSOption   = "dns_option"
	repeatedDNSSearch   = "dns_search"
	repeatedDevice      = "device"
	repeatedExtraHost   = "extra_host"
	repeatedGroup       = "group"
	repeatedSysctl      = "sysctl"
	repeatedTmpfs       = "tmpfs"
	repeatedUlimit      = "ulimit"
	repeatedEnvironment = "environment"
	repeatedEnvFile     = "env_file"
	repeatedExpose      = "expose"
	repeatedLabel       = "label"
	repeatedPort        = "port"
	repeatedSecurity    = "security"
	repeatedVolume      = "volume"

	healthCommand       = "command"
	healthInterval      = "interval"
	healthRetries       = "retries"
	healthStartInterval = "start_interval"
	healthStartPeriod   = "start_period"
	healthTimeout       = "timeout"
)

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
	case "--expose":
		return repeatedExpose, true
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

func healthOption(name string) (string, bool) {
	switch name {
	case "--health-cmd":
		return healthCommand, true
	case "--health-interval":
		return healthInterval, true
	case "--health-retries":
		return healthRetries, true
	case "--health-start-interval":
		return healthStartInterval, true
	case "--health-start-period":
		return healthStartPeriod, true
	case "--health-timeout":
		return healthTimeout, true
	default:
		return "", false
	}
}

func validPullPolicy(value string) bool {
	return value == "always" || value == pullPolicyMissing || value == "never"
}

func validAttach(value string) bool {
	return value == "stdin" || value == "stdout" || value == "stderr"
}
