package runtimeargv

const (
	booleanInit        = "init"
	booleanInteractive = "interactive"
	booleanOOMKill     = "oom_kill"
	booleanReadOnly    = "read_only"
	booleanTTY         = "tty"

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
