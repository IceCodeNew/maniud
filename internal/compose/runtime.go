package compose

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	composetypes "github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v4"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const (
	composeBridgeNetwork = "bridge"
	composeProtocolTCP   = "tcp"
	composeProtocolUDP   = "udp"
)

// RenderRuntime binds a parsed runtime projection to an immutable image, then
// serializes and validates the resulting Compose document in memory.
func RenderRuntime(
	ctx context.Context,
	projection runtimeargv.Projection,
	image domain.ImageIdentity,
	workingDirectory string,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("render runtime Compose: %w", err)
	}
	workload, err := projection.Workload(image)
	if err != nil {
		return nil, fmt.Errorf("bind runtime Compose: %w", err)
	}
	service := runtimeServiceFromWorkload(workload, image.Reference)
	service.EnvFile, err = runtimeEnvironmentFiles(projection.EnvironmentFiles(), workingDirectory)
	if err != nil {
		return nil, fmt.Errorf("bind runtime environment files: %w", err)
	}
	document := runtimeDocument{
		Name: workload.ServiceName,
		Services: map[string]runtimeService{
			workload.ServiceName: service,
		},
	}
	if runtimeName := projection.Runtime(); runtimeName != "" && runtimeName != composeDockerRuntime {
		document.Maniud = &runtimeExtension{Services: map[string]runtimeMetadata{
			workload.ServiceName: {Runtime: runtimeName},
		}}
	}

	return renderRuntimeDocument(ctx, document, projection.Name(), workingDirectory)
}

// RenderArchive serializes an analyzed Docker archive through the same
// runtime-neutral workload projection used by registry and argv generation.
func RenderArchive(
	ctx context.Context,
	analysis imagearchive.Analysis,
	explicitName string,
	workingDirectory string,
) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", fmt.Errorf("render archive Compose: %w", err)
	}
	name, err := analysis.ServiceName(explicitName)
	if err != nil {
		return nil, "", fmt.Errorf("select archive service: %w", err)
	}
	workload := domain.ResolveWorkloadSpec(domain.WorkloadSpec{
		ServiceName: name, ContainerName: name, Platform: analysis.Identity.Platform,
		NetworkMode: composeBridgeNetwork,
	}, analysis.Identity)
	service := runtimeServiceFromWorkload(workload, analysis.ComposeReference)
	service.PullPolicy = "never"
	document := runtimeDocument{
		Name: name,
		Services: map[string]runtimeService{
			name: service,
		},
		Maniud: &runtimeExtension{Services: map[string]runtimeMetadata{
			name: {ImageSource: runtimeArchiveMetadata(analysis)},
		}},
	}
	rendered, err := renderRuntimeDocument(ctx, document, name, workingDirectory)
	if err == nil {
		err = validateRenderedArchive(ctx, rendered, workingDirectory, name, workload)
	}
	if err != nil {
		return nil, "", err
	}

	return rendered, name, nil
}

func validateRenderedArchive(
	ctx context.Context,
	rendered []byte,
	workingDirectory string,
	name string,
	expected domain.WorkloadSpec,
) error {
	project, err := Load(ctx, Source{
		Content: rendered, WorkingDir: workingDirectory, Environment: map[string]string{},
	})
	if err != nil {
		return fmt.Errorf("validate generated archive Compose: %w", err)
	}
	input, err := project.ImageInput(name)
	if err != nil {
		return fmt.Errorf("validate generated archive image: %w", err)
	}
	image, valid := input.ArchiveIdentity()
	if !valid {
		return ErrInvalidSource
	}
	workload, err := project.Workload(name, image)
	if err != nil || !reflect.DeepEqual(workload.WorkloadSpec, expected) {
		return ErrInvalidSource
	}

	return nil
}

func renderRuntimeDocument(
	ctx context.Context,
	document runtimeDocument,
	projectName string,
	workingDirectory string,
) ([]byte, error) {
	// runtimeDocument contains only acyclic scalar, slice, map, and struct
	// fields; its two custom marshalers always return nil errors.
	rendered, _ := yaml.Marshal(document)
	_, err := loader.LoadWithContext(ctx, composetypes.ConfigDetails{
		WorkingDir: workingDirectory,
		ConfigFiles: []composetypes.ConfigFile{{
			Filename: filepath.Join(workingDirectory, "compose.yaml"),
			Content:  rendered,
		}},
		Environment: composetypes.Mapping{},
	}, func(options *loader.Options) {
		options.SetProjectName(projectName, true)
		withoutSecondaryReads(options)
	})

	return finishRuntimeRender(rendered, err)
}

func finishRuntimeRender(rendered []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, fmt.Errorf("validate generated Compose: %w", err)
	}
	// Keep generated documents within the same single-file limit enforced when
	// apply captures them from Git.
	if len(rendered) > maxSourceBytes {
		return nil, ErrInvalidSource
	}

	return rendered, nil
}

//nolint:tagliatelle // The Compose specification defines these snake-case YAML keys.
type runtimeService struct {
	Image           string                   `yaml:"image"`
	ContainerName   string                   `yaml:"container_name"`
	Platform        string                   `yaml:"platform"`
	Command         []string                 `yaml:"command,omitempty"`
	Entrypoint      []string                 `yaml:"entrypoint,omitempty"`
	NetworkMode     string                   `yaml:"network_mode"`
	BlkioConfig     *runtimeBlkioConfig      `yaml:"blkio_config,omitempty"`
	CgroupParent    string                   `yaml:"cgroup_parent,omitempty"`
	Cgroup          string                   `yaml:"cgroup,omitempty"`
	CPUs            string                   `yaml:"cpus,omitempty"`
	Hostname        string                   `yaml:"hostname,omitempty"`
	MemLimit        int64                    `yaml:"mem_limit,omitempty"`
	OOMScoreAdj     *int                     `yaml:"oom_score_adj,omitempty"`
	PidsLimit       *int64                   `yaml:"pids_limit,omitempty"`
	Restart         string                   `yaml:"restart,omitempty"`
	ShmSize         int64                    `yaml:"shm_size,omitempty"`
	StopSignal      string                   `yaml:"stop_signal,omitempty"`
	StopGracePeriod string                   `yaml:"stop_grace_period,omitempty"`
	User            string                   `yaml:"user,omitempty"`
	WorkingDir      string                   `yaml:"working_dir,omitempty"`
	CapAdd          []string                 `yaml:"cap_add,omitempty"`
	CapDrop         []string                 `yaml:"cap_drop,omitempty"`
	DNS             []string                 `yaml:"dns,omitempty"`
	DNSOpt          []string                 `yaml:"dns_opt,omitempty"`
	DNSSearch       []string                 `yaml:"dns_search,omitempty"`
	Devices         []runtimeDeviceMapping   `yaml:"devices,omitempty"`
	ExtraHosts      []string                 `yaml:"extra_hosts,omitempty"`
	GroupAdd        []string                 `yaml:"group_add,omitempty"`
	Sysctls         map[string]string        `yaml:"sysctls,omitempty"`
	Tmpfs           []string                 `yaml:"tmpfs,omitempty"`
	Ulimits         map[string]runtimeUlimit `yaml:"ulimits,omitempty"`
	Environment     []string                 `yaml:"environment,omitempty"`
	EnvFile         []string                 `yaml:"env_file,omitempty"`
	Expose          []string                 `yaml:"expose,omitempty"`
	Labels          []string                 `yaml:"labels,omitempty"`
	Ports           []string                 `yaml:"ports,omitempty"`
	PullPolicy      string                   `yaml:"pull_policy,omitempty"`
	SecurityOpt     []string                 `yaml:"security_opt,omitempty"`
	Volumes         []runtimeMount           `yaml:"volumes,omitempty"`
	Init            *bool                    `yaml:"init,omitempty"`
	StdinOpen       *bool                    `yaml:"stdin_open,omitempty"`
	OOMKillDisable  *bool                    `yaml:"oom_kill_disable,omitempty"`
	ReadOnly        *bool                    `yaml:"read_only,omitempty"`
	TTY             *bool                    `yaml:"tty,omitempty"`
	Healthcheck     *runtimeHealthcheck      `yaml:"healthcheck,omitempty"`
}

type runtimeBlkioConfig struct {
	Weight int `yaml:"weight"`
}

type runtimeDeviceMapping struct {
	Source      string `yaml:"source"`
	Target      string `yaml:"target"`
	Permissions string `yaml:"permissions"`
}

type runtimeMount struct {
	short string
	bind  *runtimeBindMount
}

func (value runtimeMount) MarshalYAML() (any, error) {
	if value.bind != nil {
		return *value.bind, nil
	}

	return value.short, nil
}

type runtimeBindMount struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"` //nolint:tagliatelle // Compose defines this key.
}

type runtimeUlimit struct {
	Soft int64
	Hard int64
}

func (value runtimeUlimit) MarshalYAML() (any, error) {
	if value.Soft == value.Hard {
		return value.Soft, nil
	}

	return struct {
		Soft int64 `yaml:"soft"`
		Hard int64 `yaml:"hard"`
	}{Soft: value.Soft, Hard: value.Hard}, nil
}

//nolint:tagliatelle // The Compose specification defines these snake-case YAML keys.
type runtimeHealthcheck struct {
	Test          []string `yaml:"test,omitempty"`
	Interval      string   `yaml:"interval,omitempty"`
	Timeout       string   `yaml:"timeout,omitempty"`
	Retries       *int     `yaml:"retries,omitempty"`
	StartPeriod   string   `yaml:"start_period,omitempty"`
	StartInterval string   `yaml:"start_interval,omitempty"`
	Disable       bool     `yaml:"disable,omitempty"`
}

//nolint:tagliatelle // The Compose specification defines the x-maniud extension key.
type runtimeDocument struct {
	Name     string                    `yaml:"name"`
	Services map[string]runtimeService `yaml:"services"`
	Maniud   *runtimeExtension         `yaml:"x-maniud,omitempty"`
}

type runtimeExtension struct {
	Services map[string]runtimeMetadata `yaml:"services"`
}

type runtimeMetadata struct {
	Runtime     string                `yaml:"runtime,omitempty"`
	ImageSource *runtimeArchiveSource `yaml:"image_source,omitempty"` //nolint:tagliatelle // Compose extension key.
}

//nolint:tagliatelle // The Compose extension defines these snake-case YAML keys.
type runtimeArchiveSource struct {
	Kind                   string `yaml:"kind"`
	Selector               string `yaml:"selector"`
	ArchiveDigest          string `yaml:"archive_digest"`
	ArchiveSize            int64  `yaml:"archive_size"`
	ArchiveManifestDigest  string `yaml:"archive_manifest_digest"`
	ArchiveMemberIndex     int    `yaml:"archive_member_index"`
	Platform               string `yaml:"platform"`
	SourceReference        string `yaml:"source_reference,omitempty"`
	ReferenceDigest        string `yaml:"reference_digest"`
	PlatformManifestDigest string `yaml:"platform_manifest_digest"`
	ImageConfigDigest      string `yaml:"image_config_digest"`
}

func runtimeServiceFromWorkload(workload domain.WorkloadSpec, image string) runtimeService {
	service := runtimeService{
		Image: image, ContainerName: workload.ContainerName, Platform: formatPlatform(workload.Platform),
		Command: workload.Command, Entrypoint: workload.Entrypoint, NetworkMode: workload.NetworkMode,
		CgroupParent: workload.CgroupParent, Cgroup: workload.Cgroup, CPUs: workload.CPUs,
		Hostname: workload.Hostname, MemLimit: workload.MemoryBytes, OOMScoreAdj: workload.OOMScoreAdj,
		PidsLimit: workload.PidsLimit, Restart: workload.Restart, ShmSize: workload.SharedMemoryBytes,
		StopSignal: workload.StopSignal, User: workload.User, WorkingDir: workload.WorkingDirectory,
		CapAdd: workload.CapAdd, CapDrop: workload.CapDrop, DNS: workload.DNS,
		DNSOpt: workload.DNSOptions, DNSSearch: workload.DNSSearch,
		ExtraHosts: workload.ExtraHosts, GroupAdd: workload.GroupAdd, Sysctls: workload.Sysctls,
		Environment: workload.Environment, Expose: runtimeExposedPorts(workload.ExposedPorts), Labels: workload.Labels,
		Init: workload.Init, StdinOpen: workload.StdinOpen, OOMKillDisable: workload.OOMKillDisable,
		ReadOnly: workload.ReadOnly, TTY: workload.TTY,
	}
	if workload.BlkioWeight != nil {
		service.BlkioConfig = &runtimeBlkioConfig{Weight: *workload.BlkioWeight}
	}
	if workload.StopTimeout != nil {
		service.StopGracePeriod = strconv.FormatInt(*workload.StopTimeout, 10) + "s"
	}
	service.Devices = runtimeDevices(workload.Devices)
	service.Tmpfs = runtimeTmpfs(workload.Tmpfs)
	service.Ulimits = runtimeUlimits(workload.Ulimits)
	service.Ports = runtimePorts(workload.Ports)
	service.Volumes = runtimeMounts(workload.Mounts)
	if workload.NoNewPrivileges {
		service.SecurityOpt = []string{"no-new-privileges:true"}
	}
	if workload.Healthcheck != nil {
		service.Healthcheck = &runtimeHealthcheck{
			Test: workload.Healthcheck.Test, Interval: workload.Healthcheck.Interval,
			Timeout: workload.Healthcheck.Timeout, Retries: workload.Healthcheck.Retries,
			StartPeriod: workload.Healthcheck.StartPeriod, StartInterval: workload.Healthcheck.StartInterval,
			Disable: workload.Healthcheck.Disabled,
		}
	}

	return service
}

func runtimeArchiveMetadata(analysis imagearchive.Analysis) *runtimeArchiveSource {
	return &runtimeArchiveSource{
		Kind: archiveKind, Selector: analysis.Source.Selector(),
		ArchiveDigest: analysis.ArchiveDigest.String(), ArchiveSize: analysis.ArchiveSize,
		ArchiveManifestDigest: analysis.ManifestDigest.String(), ArchiveMemberIndex: analysis.MemberIndex,
		Platform: formatPlatform(analysis.Identity.Platform), SourceReference: analysis.SourceReference,
		ReferenceDigest:        analysis.Identity.ReferenceDigest.String(),
		PlatformManifestDigest: analysis.Identity.PlatformManifest.String(),
		ImageConfigDigest:      analysis.Identity.ImageConfig.String(),
	}
}

func formatPlatform(platform domain.Platform) string {
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}

	return value
}

func runtimeEnvironmentFiles(values []string, workingDirectory string) ([]string, error) {
	if values == nil {
		return nil, nil
	}
	result := make([]string, len(values))
	for index, value := range values {
		relative, err := filepath.Rel(workingDirectory, value)
		if err != nil || relative == "." || filepath.IsAbs(relative) {
			return nil, runtimeargv.ErrInvalid
		}
		result[index] = filepath.ToSlash(relative)
	}

	return result, nil
}

func runtimeDevices(values []domain.DeviceMapping) []runtimeDeviceMapping {
	result := make([]runtimeDeviceMapping, len(values))
	for index, value := range values {
		result[index] = runtimeDeviceMapping(value)
	}

	return result
}

func runtimeTmpfs(values []domain.TmpfsMount) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.Target
		if len(value.Options) != 0 {
			result[index] += ":" + strings.Join(value.Options, ",")
		}
	}

	return result
}

func runtimeUlimits(values []domain.Ulimit) map[string]runtimeUlimit {
	if values == nil {
		return nil
	}
	result := make(map[string]runtimeUlimit, len(values))
	for _, value := range values {
		result[value.Name] = runtimeUlimit{Soft: value.Soft, Hard: value.Hard}
	}

	return result
}

func runtimePorts(values []domain.PortBinding) []string {
	result := make([]string, len(values))
	for index, value := range values {
		host := value.HostIP
		if strings.ContainsRune(host, ':') {
			host = "[" + host + "]"
		}
		if host != "" {
			host += ":"
		}
		result[index] = host + strconv.FormatUint(uint64(value.PublishedPort), 10) + ":" +
			strconv.FormatUint(uint64(value.TargetPort), 10)
		if value.Protocol != composeProtocolTCP {
			result[index] += "/" + value.Protocol
		}
	}

	return result
}

func runtimeExposedPorts(values []domain.ExposedPort) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strconv.FormatUint(uint64(value.TargetPort), 10) + "/" + value.Protocol
	}

	return result
}

func runtimeMounts(values []domain.Mount) []runtimeMount {
	result := make([]runtimeMount, len(values))
	for index, value := range values {
		if value.Kind == domain.MountVolume {
			result[index] = runtimeMount{short: value.Target}

			continue
		}
		result[index] = runtimeMount{bind: &runtimeBindMount{
			Type: composeBindMountType, Source: value.Source, Target: value.Target, ReadOnly: value.ReadOnly,
		}}
	}

	return result
}
