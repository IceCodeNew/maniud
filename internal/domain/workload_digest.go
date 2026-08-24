package domain

import (
	"encoding/binary"
	"maps"
	"slices"
)

const (
	effectiveWorkloadVersion = 1
	workloadSpecVersion      = 1
	storageDigestVersion     = 1
	encodedFalse             = byte(0)
	encodedTrue              = byte(1)
	encodedOptionalTrue      = byte(2)
)

// ComputeStorageDigest binds desired Git provenance and mount policy to the
// exact persistent sources reported by a runtime. It rejects any missing,
// additional, duplicate-target, or policy-conflicting observation.
//
//nolint:cyclop // Every desired/runtime mount identity invariant fails closed independently.
func ComputeStorageDigest(workload DesiredWorkload, observed []RuntimeMount) (Digest, bool) {
	if workload.SourceDigest == (Digest{}) {
		return Digest{}, false
	}
	desired := make(map[string]Mount, len(workload.Mounts))
	for _, value := range workload.Mounts {
		if value.Target == "" {
			return Digest{}, false
		}
		if _, duplicate := desired[value.Target]; duplicate {
			return Digest{}, false
		}
		desired[value.Target] = value
	}
	ordered := slices.Clone(observed)
	slices.SortFunc(ordered, func(left, right RuntimeMount) int {
		if left.Target < right.Target {
			return -1
		}
		if left.Target > right.Target {
			return 1
		}

		return 0
	})
	encoded := []byte{storageDigestVersion}
	encoded = append(encoded, workload.SourceDigest[:]...)
	encoded = binary.AppendUvarint(encoded, uint64(len(ordered)))
	for index, value := range ordered {
		if index > 0 && ordered[index-1].Target == value.Target {
			return Digest{}, false
		}
		expected, found := desired[value.Target]
		if !found || value.Kind != expected.Kind || value.ReadOnly != expected.ReadOnly || value.Source == "" {
			return Digest{}, false
		}
		switch value.Kind {
		case MountBind:
			if value.Name != "" || value.Source != expected.Source {
				return Digest{}, false
			}
		case MountVolume:
			if value.Name == "" || expected.Source != "" || expected.ReadOnly {
				return Digest{}, false
			}
		default:
			return Digest{}, false
		}
		delete(desired, value.Target)
		encoded = append(encoded, byte(value.Kind))
		encoded = appendString(encoded, value.Name)
		encoded = appendString(encoded, value.Source)
		encoded = appendString(encoded, value.Target)
		encoded = appendBool(encoded, value.ReadOnly)
	}
	if len(desired) != 0 {
		return Digest{}, false
	}

	return Hash(encoded), true
}

// ComputeWorkloadSpecDigest identifies the complete resolved container
// configuration independently from its image and source evidence.
func ComputeWorkloadSpecDigest(spec WorkloadSpec) Digest {
	spec.ServiceName = ""
	spec.ContainerName = ""
	spec.Platform = Platform{}

	return Hash(appendWorkloadSpec([]byte{workloadSpecVersion}, spec))
}

// ComputeEffectiveDigest identifies every runtime-relevant field in one
// resolved workload. SourceDigest and EffectiveDigest are evidence about the
// workload and are intentionally excluded from the identity itself.
func ComputeEffectiveDigest(workload DesiredWorkload) Digest {
	encoded := []byte{effectiveWorkloadVersion}
	encoded = appendWorkloadSpec(encoded, workload.WorkloadSpec)
	encoded = appendImageIdentity(encoded, workload.Image)

	return Hash(encoded)
}

func appendWorkloadSpec(encoded []byte, spec WorkloadSpec) []byte {
	encoded = appendString(encoded, spec.ServiceName)
	encoded = appendString(encoded, spec.ContainerName)
	encoded = appendPlatform(encoded, spec.Platform)
	encoded = appendStrings(encoded, spec.Entrypoint)
	encoded = appendStrings(encoded, spec.Command)
	encoded = appendString(encoded, spec.NetworkMode)
	encoded = appendOptionalInteger(encoded, spec.BlkioWeight)
	encoded = appendString(encoded, spec.CgroupParent)
	encoded = appendString(encoded, spec.Cgroup)
	encoded = appendString(encoded, spec.CPUs)
	encoded = appendString(encoded, spec.Hostname)
	encoded = binary.AppendVarint(encoded, spec.MemoryBytes)
	encoded = appendOptionalInteger(encoded, spec.OOMScoreAdj)
	encoded = appendOptionalInteger(encoded, spec.PidsLimit)
	encoded = appendString(encoded, spec.Restart)
	encoded = binary.AppendVarint(encoded, spec.SharedMemoryBytes)
	encoded = appendString(encoded, spec.StopSignal)
	encoded = appendOptionalInteger(encoded, spec.StopTimeout)
	encoded = appendString(encoded, spec.User)
	encoded = appendString(encoded, spec.WorkingDirectory)
	encoded = appendStrings(encoded, spec.CapAdd)
	encoded = appendStrings(encoded, spec.CapDrop)
	encoded = appendStrings(encoded, spec.DNS)
	encoded = appendStrings(encoded, spec.DNSOptions)
	encoded = appendStrings(encoded, spec.DNSSearch)
	encoded = appendDevices(encoded, spec.Devices)
	encoded = appendStrings(encoded, spec.ExtraHosts)
	encoded = appendStrings(encoded, spec.GroupAdd)
	encoded = appendStringMap(encoded, spec.Sysctls)
	encoded = appendTmpfs(encoded, spec.Tmpfs)
	encoded = appendUlimits(encoded, spec.Ulimits)
	encoded = appendStrings(encoded, spec.Environment)
	encoded = appendStrings(encoded, spec.Labels)
	encoded = appendExposedPorts(encoded, spec.ExposedPorts)
	encoded = appendPorts(encoded, spec.Ports)
	encoded = appendBool(encoded, spec.NoNewPrivileges)
	encoded = appendMounts(encoded, spec.Mounts)

	return appendWorkloadOptions(encoded, spec)
}

func appendWorkloadOptions(encoded []byte, spec WorkloadSpec) []byte {
	encoded = appendOptionalBool(encoded, spec.Init)
	encoded = appendOptionalBool(encoded, spec.StdinOpen)
	encoded = appendOptionalBool(encoded, spec.OOMKillDisable)
	encoded = appendOptionalBool(encoded, spec.ReadOnly)
	encoded = appendOptionalBool(encoded, spec.TTY)

	return appendHealthcheck(encoded, spec.Healthcheck)
}

func appendExposedPorts(encoded []byte, values []ExposedPort) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = binary.AppendUvarint(encoded, uint64(value.TargetPort))
		encoded = appendString(encoded, value.Protocol)
	}

	return encoded
}

func appendImageIdentity(encoded []byte, image ImageIdentity) []byte {
	encoded = append(encoded, byte(image.Origin))
	encoded = appendString(encoded, image.Reference)
	encoded = append(encoded, image.ReferenceDigest[:]...)
	encoded = appendPlatform(encoded, image.Platform)
	encoded = append(encoded, image.PlatformManifest[:]...)
	encoded = append(encoded, image.ImageConfig[:]...)

	return encoded
}

func appendPlatform(encoded []byte, platform Platform) []byte {
	encoded = appendString(encoded, platform.OS)
	encoded = appendString(encoded, platform.Architecture)

	return appendString(encoded, platform.Variant)
}

func appendString(encoded []byte, value string) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(value)))

	return append(encoded, value...)
}

func appendStrings(encoded []byte, values []string) []byte {
	if values == nil {
		return append(encoded, 0)
	}
	encoded = append(encoded, 1)
	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = appendString(encoded, value)
	}

	return encoded
}

func appendOptionalInteger[T ~int | ~int64](encoded []byte, value *T) []byte {
	if value == nil {
		return append(encoded, 0)
	}

	return binary.AppendVarint(append(encoded, 1), int64(*value))
}

func appendBool(encoded []byte, value bool) []byte {
	if value {
		return append(encoded, encodedTrue)
	}

	return append(encoded, encodedFalse)
}

func appendOptionalBool(encoded []byte, value *bool) []byte {
	if value == nil {
		return append(encoded, encodedFalse)
	}
	if *value {
		return append(encoded, encodedOptionalTrue)
	}

	return append(encoded, encodedTrue)
}

func appendDevices(encoded []byte, values []DeviceMapping) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = appendString(encoded, value.Source)
		encoded = appendString(encoded, value.Target)
		encoded = appendString(encoded, value.Permissions)
	}

	return encoded
}

func appendStringMap(encoded []byte, values map[string]string) []byte {
	if values == nil {
		return append(encoded, 0)
	}
	encoded = append(encoded, 1)
	keys := slices.Sorted(maps.Keys(values))
	encoded = binary.AppendUvarint(encoded, uint64(len(keys)))
	for _, key := range keys {
		encoded = appendString(encoded, key)
		encoded = appendString(encoded, values[key])
	}

	return encoded
}

func appendTmpfs(encoded []byte, values []TmpfsMount) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = appendString(encoded, value.Target)
		encoded = appendStrings(encoded, value.Options)
	}

	return encoded
}

func appendUlimits(encoded []byte, values []Ulimit) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = appendString(encoded, value.Name)
		encoded = binary.AppendVarint(encoded, value.Soft)
		encoded = binary.AppendVarint(encoded, value.Hard)
	}

	return encoded
}

func appendPorts(encoded []byte, values []PortBinding) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = appendString(encoded, value.HostIP)
		encoded = binary.AppendUvarint(encoded, uint64(value.PublishedPort))
		encoded = binary.AppendUvarint(encoded, uint64(value.TargetPort))
		encoded = appendString(encoded, value.Protocol)
	}

	return encoded
}

func appendMounts(encoded []byte, values []Mount) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = append(encoded, byte(value.Kind))
		encoded = appendString(encoded, value.Source)
		encoded = appendString(encoded, value.Target)
		encoded = appendBool(encoded, value.ReadOnly)
	}

	return encoded
}

func appendHealthcheck(encoded []byte, value *Healthcheck) []byte {
	if value == nil {
		return append(encoded, 0)
	}
	encoded = append(encoded, 1)
	encoded = appendStrings(encoded, value.Test)
	encoded = appendString(encoded, value.Interval)
	encoded = appendString(encoded, value.Timeout)
	encoded = appendOptionalInteger(encoded, value.Retries)
	encoded = appendString(encoded, value.StartPeriod)
	encoded = appendString(encoded, value.StartInterval)

	return appendBool(encoded, value.Disabled)
}
