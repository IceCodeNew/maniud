package nerdctl

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/containerconfig/runtimeargv"
)

const nerdctlRuntime = runtimeargv.RuntimeNerdctl

func encodeCommand(command Command) []string {
	spec := command.Spec
	arguments := []string{nerdctlRuntime, command.Operation}
	arguments = appendScalarOptions(arguments, spec)
	arguments = appendBooleanOptions(arguments, spec)
	arguments = appendCollectionOptions(arguments, spec, command.EnvironmentFiles)
	arguments = appendHealthOptions(arguments, spec.Healthcheck)
	arguments = append(arguments, command.Image.String())
	arguments = append(arguments, spec.Command...)

	return arguments
}

func appendScalarOptions(arguments []string, spec containerconfig.Spec) []string {
	if spec.BlkioWeight != nil {
		arguments = append(arguments, "--blkio-weight="+strconv.Itoa(*spec.BlkioWeight))
	}
	arguments = appendStringOption(arguments, "--cgroup-parent", spec.CgroupParent)
	arguments = appendStringOption(arguments, "--cgroupns", spec.Cgroup)
	arguments = appendStringOption(arguments, "--cpus", spec.CPUs)
	if len(spec.Entrypoint) == 1 {
		arguments = append(arguments, "--entrypoint="+spec.Entrypoint[0])
	}
	arguments = appendStringOption(arguments, "--hostname", spec.Hostname)
	if spec.MemoryBytes != 0 {
		arguments = append(arguments, "--memory="+strconv.FormatInt(spec.MemoryBytes, 10))
	}
	arguments = append(arguments, "--name="+spec.ContainerName)
	arguments = append(arguments, "--network="+spec.NetworkMode)
	if spec.OOMScoreAdj != nil {
		arguments = append(arguments, "--oom-score-adj="+strconv.Itoa(*spec.OOMScoreAdj))
	}
	arguments = append(arguments, "--platform="+platformString(spec.Platform))
	if spec.PidsLimit != nil {
		arguments = append(arguments, "--pids-limit="+strconv.FormatInt(*spec.PidsLimit, 10))
	}
	arguments = appendStringOption(arguments, "--restart", spec.Restart)
	if spec.SharedMemoryBytes != 0 {
		arguments = append(arguments, "--shm-size="+strconv.FormatInt(spec.SharedMemoryBytes, 10))
	}
	arguments = appendStringOption(arguments, "--stop-signal", spec.StopSignal)
	if spec.StopTimeout != nil {
		arguments = append(arguments, "--stop-timeout="+strconv.FormatInt(*spec.StopTimeout, 10))
	}
	arguments = appendStringOption(arguments, "--user", spec.User)

	return appendStringOption(arguments, "--workdir", spec.WorkingDirectory)
}

func appendStringOption(arguments []string, name, value string) []string {
	if value == "" {
		return arguments
	}

	return append(arguments, name+"="+value)
}

func appendBooleanOptions(arguments []string, spec containerconfig.Spec) []string {
	arguments = appendBoolOption(arguments, "--init", spec.Init, true)
	arguments = appendBoolOption(arguments, "--interactive", spec.StdinOpen, false)
	arguments = appendBoolOption(arguments, "--oom-kill-disable", spec.OOMKillDisable, false)
	arguments = appendBoolOption(arguments, "--read-only", spec.ReadOnly, false)

	return appendBoolOption(arguments, "--tty", spec.TTY, false)
}

func appendBoolOption(arguments []string, name string, value *bool, preserveFalse bool) []string {
	if value == nil || !*value && !preserveFalse {
		return arguments
	}

	return append(arguments, name+"="+strconv.FormatBool(*value))
}

func appendCollectionOptions(
	arguments []string,
	spec containerconfig.Spec,
	environmentFiles []string,
) []string {
	arguments = appendStrings(arguments, "--cap-add", spec.CapAdd)
	arguments = appendStrings(arguments, "--cap-drop", spec.CapDrop)
	arguments = appendStrings(arguments, "--dns", spec.DNS)
	arguments = appendStrings(arguments, "--dns-option", spec.DNSOptions)
	arguments = appendStrings(arguments, "--dns-search", spec.DNSSearch)
	for _, device := range spec.Devices {
		arguments = append(arguments, "--device="+device.Source+":"+device.Target+":"+device.Permissions)
	}
	arguments = appendStrings(arguments, "--add-host", spec.ExtraHosts)
	arguments = appendStrings(arguments, "--group-add", spec.GroupAdd)
	for _, name := range sortedMapKeys(spec.Sysctls) {
		arguments = append(arguments, "--sysctl="+name+"="+spec.Sysctls[name])
	}
	for _, tmpfs := range spec.Tmpfs {
		value := tmpfs.Target
		if len(tmpfs.Options) != 0 {
			value += ":" + strings.Join(tmpfs.Options, ",")
		}
		arguments = append(arguments, "--tmpfs="+value)
	}
	for _, limit := range spec.Ulimits {
		value := limit.Name + "=" + strconv.FormatInt(limit.Soft, 10) + ":" + strconv.FormatInt(limit.Hard, 10)
		arguments = append(arguments, "--ulimit="+value)
	}
	arguments = appendStrings(arguments, "--env", spec.Environment)
	arguments = appendStrings(arguments, "--env-file", environmentFiles)
	for _, port := range spec.ExposedPorts {
		arguments = append(arguments, "--expose="+formatExposedPort(port))
	}
	arguments = appendStrings(arguments, "--label", spec.Labels)
	for _, port := range spec.Ports {
		arguments = append(arguments, "--publish="+formatPort(port))
	}
	if spec.NoNewPrivileges {
		arguments = append(arguments, "--security-opt=no-new-privileges")
	}

	return appendMountOptions(arguments, spec.Mounts)
}

func appendMountOptions(arguments []string, mounts []containerconfig.Mount) []string {
	for _, mount := range mounts {
		if mount.Kind == containerconfig.MountVolume {
			arguments = append(arguments, "--volume="+mount.Target)

			continue
		}
		value := "type=bind,source=" + mount.Source + ",target=" + mount.Target
		if mount.ReadOnly {
			value += ",readonly"
		}
		arguments = append(arguments, "--mount="+value)
	}

	return arguments
}

func appendStrings(arguments []string, option string, values []string) []string {
	for _, value := range values {
		arguments = append(arguments, option+"="+value)
	}

	return arguments
}

func appendHealthOptions(arguments []string, health *containerconfig.Healthcheck) []string {
	if health == nil {
		return arguments
	}
	if health.Disabled {
		return append(arguments, "--no-healthcheck")
	}
	if len(health.Test) == 2 && health.Test[0] == "CMD-SHELL" {
		arguments = append(arguments, "--health-cmd="+health.Test[1])
	}
	arguments = appendStringOption(arguments, "--health-interval", health.Interval)
	arguments = appendStringOption(arguments, "--health-timeout", health.Timeout)
	if health.Retries != nil {
		arguments = append(arguments, "--health-retries="+strconv.Itoa(*health.Retries))
	}

	return appendStringOption(arguments, "--health-start-period", health.StartPeriod)
}

func platformString(platform containerconfig.Platform) string {
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}

	return value
}

func formatPort(port containerconfig.PortBinding) string {
	prefix := ""
	if port.HostIP != "" {
		prefix = port.HostIP + ":"
		if address, err := netip.ParseAddr(port.HostIP); err == nil && address.Is6() {
			prefix = "[" + address.String() + "]:"
		}
	}

	return fmt.Sprintf("%s%d:%d/%s", prefix, port.PublishedPort, port.TargetPort, port.Protocol)
}

func formatExposedPort(port containerconfig.ExposedPort) string {
	return strconv.FormatUint(uint64(port.TargetPort), 10) + "/" + port.Protocol
}

func sortedMapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	return keys
}
