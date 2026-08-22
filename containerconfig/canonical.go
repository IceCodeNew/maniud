package containerconfig

import (
	"cmp"
	"reflect"
	"slices"
)

// Canonical returns an owned stable representation of portable unordered
// fields. It does not validate the specification, remove duplicates, apply
// runtime defaults, or reorder fields whose sequence affects behavior.
func Canonical(spec Spec) Spec {
	canonical := spec.Clone()
	canonical.CapAdd = canonicalStrings(canonical.CapAdd)
	canonical.CapDrop = canonicalStrings(canonical.CapDrop)
	canonical.Devices = canonicalSlice(canonical.Devices, compareDevice)
	canonical.ExtraHosts = canonicalStrings(canonical.ExtraHosts)
	canonical.GroupAdd = canonicalStrings(canonical.GroupAdd)
	canonical.Tmpfs = canonicalSlice(canonical.Tmpfs, compareTmpfs)
	canonical.Ulimits = canonicalSlice(canonical.Ulimits, compareUlimit)
	canonical.Environment = canonicalStrings(canonical.Environment)
	canonical.Labels = canonicalStrings(canonical.Labels)
	canonical.ExposedPorts = canonicalSlice(canonical.ExposedPorts, compareExposedPort)
	canonical.Ports = canonicalSlice(canonical.Ports, comparePort)
	canonical.Mounts = canonicalSlice(canonical.Mounts, compareMount)
	if len(canonical.Sysctls) == 0 {
		canonical.Sysctls = nil
	}

	return canonical
}

// Equivalent reports whether two specifications have the same portable
// desired state after canonicalizing unordered fields. Runtime adapters remain
// responsible for defaults, unobservable fields, and native wire behavior.
func Equivalent(left, right Spec) bool {
	return reflect.DeepEqual(Canonical(left), Canonical(right))
}

func canonicalStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	slices.Sort(values)

	return values
}

func canonicalSlice[E any](values []E, compare func(E, E) int) []E {
	if len(values) == 0 {
		return nil
	}
	slices.SortFunc(values, compare)

	return values
}

func compareDevice(left, right DeviceMapping) int {
	return cmp.Or(
		cmp.Compare(left.Target, right.Target),
		cmp.Compare(left.Source, right.Source),
		cmp.Compare(left.Permissions, right.Permissions),
	)
}

func compareTmpfs(left, right TmpfsMount) int {
	return cmp.Or(
		cmp.Compare(left.Target, right.Target),
		slices.Compare(left.Options, right.Options),
	)
}

func compareUlimit(left, right Ulimit) int {
	return cmp.Or(
		cmp.Compare(left.Name, right.Name),
		cmp.Compare(left.Soft, right.Soft),
		cmp.Compare(left.Hard, right.Hard),
	)
}

func compareExposedPort(left, right ExposedPort) int {
	return cmp.Or(
		cmp.Compare(left.TargetPort, right.TargetPort),
		cmp.Compare(left.Protocol, right.Protocol),
	)
}

func comparePort(left, right PortBinding) int {
	return cmp.Or(
		cmp.Compare(left.TargetPort, right.TargetPort),
		cmp.Compare(left.Protocol, right.Protocol),
		cmp.Compare(left.HostIP, right.HostIP),
		cmp.Compare(left.PublishedPort, right.PublishedPort),
	)
}

func compareMount(left, right Mount) int {
	return cmp.Or(
		cmp.Compare(left.Target, right.Target),
		cmp.Compare(left.Source, right.Source),
		cmp.Compare(left.Kind, right.Kind),
		cmp.Compare(boolOrder(left.ReadOnly), boolOrder(right.ReadOnly)),
	)
}

func boolOrder(value bool) uint8 {
	if value {
		return 1
	}

	return 0
}
