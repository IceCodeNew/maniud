package domain

import (
	"reflect"
	"slices"
	"testing"
)

const (
	digestTestArchitecture = "amd64"
	digestTestOS           = "linux"
	digestTestDataTarget   = "/data"
)

func TestEffectiveDigestIncludesEveryWorkloadField(t *testing.T) {
	t.Parallel()

	workload := DesiredWorkload{WorkloadSpec: completeDigestTestWorkload()}
	baseline := ComputeEffectiveDigest(workload)
	for _, path := range workloadDigestFieldPaths(reflect.TypeFor[WorkloadSpec](), nil, "") {
		t.Run(path.name, func(t *testing.T) {
			t.Parallel()

			changed := workload
			changed.WorkloadSpec = workload.Clone()
			mutateWorkloadDigestField(reflect.ValueOf(&changed.WorkloadSpec).Elem(), path.indices)
			if ComputeEffectiveDigest(changed) == baseline {
				t.Fatalf("ComputeEffectiveDigest() ignored WorkloadSpec.%s", path.name)
			}
		})
	}
}

func TestWorkloadDigestsPreserveVersionedIdentitySemantics(t *testing.T) {
	t.Parallel()

	zero := WorkloadSpec{}
	baseline := ComputeWorkloadSpecDigest(zero)
	ignored := zero
	ignored.ServiceName = domainTestService
	ignored.ContainerName = "api-1"
	ignored.Platform = Platform{OS: digestTestOS, Architecture: digestTestArchitecture}
	if ComputeWorkloadSpecDigest(ignored) != baseline {
		t.Fatal("workload spec digest includes deployment identity fields")
	}

	falseValue := false
	withExplicitEmpty := zero
	withExplicitEmpty.Entrypoint = []string{}
	withExplicitEmpty.Sysctls = map[string]string{}
	withExplicitEmpty.BlkioWeight = new(int)
	withExplicitEmpty.Init = &falseValue
	withExplicitEmpty.Healthcheck = &Healthcheck{}
	if ComputeWorkloadSpecDigest(withExplicitEmpty) == baseline {
		t.Fatal("workload spec digest loses nil-versus-explicit-empty semantics")
	}

	workload := DesiredWorkload{WorkloadSpec: completeDigestTestWorkload(), Image: ImageIdentity{
		Origin:           ImageOriginRegistry,
		Reference:        "example.test/app:latest",
		ReferenceDigest:  Hash([]byte("reference")),
		Platform:         Platform{OS: digestTestOS, Architecture: digestTestArchitecture},
		PlatformManifest: Hash([]byte("manifest")), ImageConfig: Hash([]byte("config")),
	}}
	effective := ComputeEffectiveDigest(workload)
	workload.SourceDigest = Hash([]byte("source evidence"))
	workload.EffectiveDigest = Hash([]byte("cached evidence"))
	if ComputeEffectiveDigest(workload) != effective {
		t.Fatal("effective digest includes evidence fields")
	}
	workload.Image.Origin = ImageOriginDockerArchive
	if ComputeEffectiveDigest(workload) == effective {
		t.Fatal("effective digest omits image origin/version semantics")
	}
}

func TestComputeStorageDigestBindsRuntimeIdentityAndGitProvenance(t *testing.T) {
	t.Parallel()

	workload := DesiredWorkload{
		WorkloadSpec: completeDigestTestWorkload(),
		SourceDigest: Hash([]byte("source evidence")),
	}
	workload.Mounts = []Mount{
		{Kind: MountBind, Source: "/state/sources/revision/data", Target: digestTestDataTarget, ReadOnly: true},
		{Kind: MountVolume, Target: "/state"},
	}
	observed := []RuntimeMount{
		{Kind: MountVolume, Name: "volume-a", Source: "/runtime/volumes/volume-a", Target: "/state"},
		{Kind: MountBind, Source: "/state/sources/revision/data", Target: digestTestDataTarget, ReadOnly: true},
	}
	digest, valid := ComputeStorageDigest(workload, observed)
	if !valid || digest == (Digest{}) {
		t.Fatalf("ComputeStorageDigest() = %s, %t", digest, valid)
	}
	changed := slices.Clone(observed)
	changed[0].Name = "volume-b"
	other, valid := ComputeStorageDigest(workload, changed)
	if !valid || other == digest {
		t.Fatalf("ComputeStorageDigest(changed volume) = %s, %t", other, valid)
	}
	changedWorkload := workload
	changedWorkload.SourceDigest = Hash([]byte("other source"))
	other, valid = ComputeStorageDigest(changedWorkload, observed)
	if !valid || other == digest {
		t.Fatalf("ComputeStorageDigest(changed provenance) = %s, %t", other, valid)
	}

	for _, invalid := range [][]RuntimeMount{
		nil,
		append(slices.Clone(observed), observed[0]),
		{{Kind: MountBind, Source: "/wrong", Target: digestTestDataTarget, ReadOnly: true}, observed[0]},
	} {
		if _, valid := ComputeStorageDigest(workload, invalid); valid {
			t.Fatalf("ComputeStorageDigest(%#v) = valid", invalid)
		}
	}
	assertStorageDigestRejectsInvalidWorkload(t, workload, observed)
}

func assertStorageDigestRejectsInvalidWorkload(
	t *testing.T,
	workload DesiredWorkload,
	observed []RuntimeMount,
) {
	t.Helper()
	invalidWorkloads := []DesiredWorkload{workload, workload, workload, workload}
	invalidObservations := [][]RuntimeMount{observed, observed, observed, {{
		Kind: MountKind(255), Source: "/runtime/unknown", Target: digestTestDataTarget,
	}}}
	for index := range invalidWorkloads {
		invalidWorkloads[index].Mounts = slices.Clone(workload.Mounts)
	}
	invalidWorkloads[0].SourceDigest = Digest{}
	invalidWorkloads[1].Mounts[0].Target = ""
	invalidWorkloads[2].Mounts = append(slices.Clone(workload.Mounts), workload.Mounts[0])
	invalidWorkloads[3].Mounts = []Mount{{Kind: MountKind(255), Target: digestTestDataTarget}}
	for index, invalidWorkload := range invalidWorkloads {
		if _, valid := ComputeStorageDigest(invalidWorkload, invalidObservations[index]); valid {
			t.Fatalf("ComputeStorageDigest(invalid workload %d) = valid", index)
		}
	}

	invalidVolumePolicy := workload
	invalidVolumePolicy.Mounts = slices.Clone(workload.Mounts)
	invalidVolumePolicy.Mounts[1].Source = "/unexpected"
	if _, valid := ComputeStorageDigest(invalidVolumePolicy, observed); valid {
		t.Fatal("ComputeStorageDigest(volume source policy) = valid")
	}
	mismatchedKind := slices.Clone(observed)
	mismatchedKind[0].Kind = MountBind
	if _, valid := ComputeStorageDigest(workload, mismatchedKind); valid {
		t.Fatal("ComputeStorageDigest(mismatched kind) = valid")
	}
}

type workloadDigestFieldPath struct {
	indices []int
	name    string
}

func workloadDigestFieldPaths(
	value reflect.Type,
	indices []int,
	name string,
) []workloadDigestFieldPath {
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if value.Kind() == reflect.Slice && value.Elem().Kind() == reflect.Struct {
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return []workloadDigestFieldPath{{indices: indices, name: name}}
	}

	var paths []workloadDigestFieldPath
	for index := range value.NumField() {
		field := value.Field(index)
		fieldName := field.Name
		if name != "" {
			fieldName = name + "." + fieldName
		}
		paths = append(paths, workloadDigestFieldPaths(
			field.Type,
			append(append([]int(nil), indices...), index),
			fieldName,
		)...)
	}

	return paths
}

func mutateWorkloadDigestField(value reflect.Value, indices []int) {
	value = workloadDigestField(value, indices)
	for value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	// The default deliberately rejects field kinds that the digest test does not know how to mutate.
	switch value.Kind() { //nolint:exhaustive // The default rejects newly introduced unsupported field kinds.
	case reflect.Bool:
		value.SetBool(!value.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		value.SetInt(value.Int() + 1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value.SetUint(value.Uint() + 1)
	case reflect.String:
		value.SetString(value.String() + "-changed")
	case reflect.Slice:
		value.Set(reflect.Append(value, reflect.Zero(value.Type().Elem())))
	case reflect.Map:
		value.SetMapIndex(reflect.ValueOf("changed"), reflect.ValueOf("value"))
	default:
		panic("unsupported WorkloadSpec field kind: " + value.Kind().String())
	}
}

func workloadDigestField(value reflect.Value, indices []int) reflect.Value {
	for position, index := range indices {
		value = value.Field(index)
		if position == len(indices)-1 {
			break
		}
		for value.Kind() == reflect.Pointer {
			value = value.Elem()
		}
		if value.Kind() == reflect.Slice {
			value = value.Index(0)
		}
	}

	return value
}

func completeDigestTestWorkload() WorkloadSpec {
	integer := 1
	integer64 := int64(1)
	truth := true

	return WorkloadSpec{
		ServiceName: "service", ContainerName: "container",
		Platform:   Platform{OS: digestTestOS, Architecture: digestTestArchitecture, Variant: "v1"},
		Entrypoint: []string{"entrypoint"}, Command: []string{"command"}, NetworkMode: "bridge",
		BlkioWeight: &integer, CgroupParent: "parent", Cgroup: "private", CPUs: "1.5", Hostname: "host",
		MemoryBytes: 1, OOMScoreAdj: &integer, PidsLimit: &integer64, Restart: "always",
		SharedMemoryBytes: 1, StopSignal: "SIGTERM", StopTimeout: &integer64,
		User: "1000", WorkingDirectory: "/work", CapAdd: []string{"NET_ADMIN"}, CapDrop: []string{"MKNOD"},
		DNS: []string{"1.1.1.1"}, DNSOptions: []string{"rotate"}, DNSSearch: []string{"example.test"},
		Devices:    []DeviceMapping{{Source: "/dev/source", Target: "/dev/target", Permissions: "rw"}},
		ExtraHosts: []string{"host=192.0.2.1"}, GroupAdd: []string{"100"}, Sysctls: map[string]string{"key": "value"},
		Tmpfs:   []TmpfsMount{{Target: "/tmp", Options: []string{"rw"}}},
		Ulimits: []Ulimit{{Name: "nofile", Soft: 1, Hard: 2}}, Environment: []string{"A=1"},
		Labels: []string{"label=value"}, ExposedPorts: []ExposedPort{{TargetPort: 80, Protocol: "tcp"}},
		Ports:           []PortBinding{{HostIP: "127.0.0.1", PublishedPort: 8080, TargetPort: 80, Protocol: "tcp"}},
		NoNewPrivileges: true, Mounts: []Mount{{Kind: MountBind, Source: "/source", Target: "/target", ReadOnly: true}},
		Init: &truth, StdinOpen: &truth, OOMKillDisable: &truth, ReadOnly: &truth, TTY: &truth,
		Healthcheck: &Healthcheck{
			Test: []string{"CMD", "true"}, Interval: "1s", Timeout: "1s", Retries: &integer,
			StartPeriod: "1s", StartInterval: "1s", Disabled: true,
		},
	}
}
