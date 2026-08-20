package podman

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	podmanExecutionEvidenceVersion = 1
	podmanConfigurationVersion     = 1
	podmanNetworkBridge            = "bridge"
	podmanCgroupPrivate            = "private"
	podmanCgroupHost               = "host"
	podmanHostAnyIPv4              = "0.0.0.0"
	podmanMountBind                = "bind"
	podmanMountVolume              = "volume"
	podmanVolumeDriverLocal        = "local"
	podmanRecursiveBind            = "rbind"
	podmanPropagationPrivate       = "rprivate"
	podmanSignalTerminateName      = "SIGTERM"
	podmanSignalInterruptName      = "SIGINT"
	podmanStateRunning             = "running"
	podmanStatePaused              = "paused"
	podmanStateRemoving            = "removing"
	podmanStateUnknown             = "unknown"
	podmanRestartAlways            = "always"
	podmanRestartOnFailure         = "on-failure"
	podmanRestartUnlessStopped     = "unless-stopped"
	podmanDefaultSharedMemory      = int64(65536000)
	podmanDefaultStopTimeout       = int64(10)
	podmanQueryFalse               = "false"
	podmanQueryTrue                = "true"
	maximumContainerNameBytes      = 63
	maximumOwnershipValueBytes     = 128
	containerIDHexBytes            = 64
	maniudLabelPrefix              = "io.maniud."
)

var (
	// ErrInvalidContainerReference reports a name or ID outside maniud's exact lookup grammar.
	ErrInvalidContainerReference = errors.New("podman container reference is invalid")

	_ application.Runtime                   = (*Client)(nil)
	_ application.ImageRuntime              = (*Client)(nil)
	_ application.WorkloadEffectRuntime     = (*Client)(nil)
	_ application.WorkloadStartRuntime      = (*Client)(nil)
	_ application.WorkloadTransitionRuntime = (*Client)(nil)
	_ application.WorkloadDiscardRuntime    = (*Client)(nil)
	_ application.WorkloadArchiveRuntime    = (*Client)(nil)
)

// ContainerState is the normalized native Libpod lifecycle used by typed probes.
type ContainerState uint8

const (
	// ContainerStateUnknown is the fail-closed zero value.
	ContainerStateUnknown ContainerState = iota
	// ContainerCreated has not started.
	ContainerCreated
	// ContainerRunning is currently executing.
	ContainerRunning
	// ContainerPaused has suspended processes.
	ContainerPaused
	// ContainerRemoving is in a transitional delete or stop state.
	ContainerRemoving
	// ContainerExited has stopped after starting.
	ContainerExited
)

// Container is runtime-neutral evidence decoded from one native Libpod inspect response.
type Container struct {
	ID               string
	Name             string
	ImageReference   string
	ImageConfig      domain.Digest
	PlatformManifest domain.Digest
	WorkloadSpec     domain.WorkloadSpec
	RuntimeMounts    []domain.RuntimeMount
	State            ContainerState
	Ownership        domain.WorkloadOwnership
}

// ContainerProbeState separates proven absence and observation from an unknown zero value.
type ContainerProbeState uint8

const (
	// ContainerProbeUnknown is returned only with an error.
	ContainerProbeUnknown ContainerProbeState = iota
	// ContainerProbeMissing proves a valid native 404 for the exact reference.
	ContainerProbeMissing
	// ContainerProbeObserved carries one strictly decoded inspect snapshot.
	ContainerProbeObserved
)

// ContainerProbe is one read-only native container conclusion.
type ContainerProbe struct {
	State     ContainerProbeState
	Container Container
}

// Inspect returns the pinned Podman daemon scope and platform.
func (client *Client) Inspect(context.Context) (application.RuntimeEvidence, error) {
	var empty application.RuntimeEvidence

	if client == nil || client.version.Protocol != libpodAPIVersion || client.scope == (domain.Digest{}) {
		return empty, ErrProtocol
	}
	platform, valid := podmanPlatform(client.version.OS, client.version.Architecture)
	if !valid {
		return empty, ErrUnsupportedWorkload
	}

	return application.RuntimeEvidence{
		Kind: domain.RuntimePodman, Platform: platform, Digest: podmanExecutionDigest(client),
	}, nil
}

func podmanExecutionDigest(client *Client) domain.Digest {
	evidence := []byte{podmanExecutionEvidenceVersion}
	evidence = appendPodmanString(evidence, domain.RuntimePodman.String())
	evidence = append(evidence, client.scope[:]...)
	evidence = appendPodmanString(evidence, client.version.Protocol)
	evidence = appendPodmanString(evidence, client.version.OS)
	evidence = appendPodmanString(evidence, client.version.Architecture)

	return domain.Hash(evidence)
}

// CheckWorkload validates one workload against the fixed native Libpod contract.
func (client *Client) CheckWorkload(workload domain.DesiredWorkload) error {
	if !validPodmanWorkload(client, workload) {
		return ErrUnsupportedWorkload
	}

	return nil
}

func validPodmanWorkload(client *Client, workload domain.DesiredWorkload) bool {
	if client == nil || client.version.Protocol != libpodAPIVersion ||
		!validPodmanImage(client, workload.Image) || !validDesiredWorkload(workload) {
		return false
	}
	_, configurationValid := podmanCreateConfiguration(
		workload, "transaction", application.WorkloadCreateOptions{CopyImageVolumes: true},
	)

	return configurationValid
}

func validPodmanImage(client *Client, image domain.ImageIdentity) bool {
	if image.Origin != domain.ImageOriginRegistry || image.ReferenceDigest == (domain.Digest{}) ||
		image.PlatformManifest == (domain.Digest{}) || image.ImageConfig == (domain.Digest{}) {
		return false
	}
	reference, err := parseExpectedImageReference(image)
	platform, valid := podmanPlatform(client.version.OS, client.version.Architecture)

	return err == nil && valid && reference.Digest() == image.ReferenceDigest && image.Platform == platform
}

func validDesiredWorkload(workload domain.DesiredWorkload) bool {
	return workload.SourceDigest != (domain.Digest{}) && workload.EffectiveDigest != (domain.Digest{}) &&
		workload.EffectiveDigest == domain.ComputeEffectiveDigest(workload) &&
		workload.Platform == workload.Image.Platform && len(workload.Entrypoint)+len(workload.Command) > 0
}

// ObserveWorkload maps one exact native probe into application planning evidence.
func (client *Client) ObserveWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
) (application.WorkloadObservation, error) {
	var empty application.WorkloadObservation

	if err := client.CheckWorkload(workload); err != nil {
		return empty, err
	}
	probe, err := client.ProbeContainer(ctx, workload.ContainerName)
	if err != nil {
		return empty, err
	}

	return podmanWorkloadObservation(probe, workload)
}

func podmanWorkloadObservation(
	probe ContainerProbe,
	workload domain.DesiredWorkload,
) (application.WorkloadObservation, error) {
	var empty application.WorkloadObservation

	switch probe.State {
	case ContainerProbeMissing:
		return application.WorkloadObservation{State: application.WorkloadObservationMissing}, nil
	case ContainerProbeObserved:
		storageDigest, valid := domain.ComputeStorageDigest(workload, probe.Container.RuntimeMounts)
		if !valid {
			return empty, ErrProtocol
		}

		return application.WorkloadObservation{
			ID:                   probe.Container.ID,
			State:                application.WorkloadObservationPresent,
			ConfigurationDigest:  containerConfigurationDigest(probe.Container),
			StorageDigest:        storageDigest,
			RuntimeMounts:        slices.Clone(probe.Container.RuntimeMounts),
			ConfigurationMatches: containerConfigurationMatches(probe.Container, workload),
			Running:              probe.Container.State == ContainerRunning,
			Ownership:            probe.Container.Ownership,
		}, nil
	case ContainerProbeUnknown:
		return empty, ErrProtocol
	default:
		return empty, ErrProtocol
	}
}

// ProbeContainer inspects one exact full ID or supported container name. Only
// a well-formed native ErrorModel 404 proves absence.
func (client *Client) ProbeContainer(ctx context.Context, reference string) (ContainerProbe, error) {
	var unknown ContainerProbe

	if !validContainerReference(reference) {
		return unknown, ErrInvalidContainerReference
	}
	response, err := client.request(
		ctx,
		http.MethodGet,
		libpodPrefix+"/containers/"+reference+"/json",
		url.Values{"size": {podmanQueryFalse}},
		nil,
		false,
	)
	if err != nil {
		return unknown, err
	}
	defer closePodmanResponse(response)

	if response.StatusCode == http.StatusNotFound {
		if !decodePodmanNotFound(response) {
			return unknown, ErrProtocol
		}

		return ContainerProbe{State: ContainerProbeMissing}, nil
	}
	if response.StatusCode != http.StatusOK || !isPodmanJSON(response.Header.Get(podmanContentType)) {
		return unknown, ErrProtocol
	}

	payload, valid := decodePodmanContainer(response.Body)
	if !valid {
		return unknown, ErrProtocol
	}
	container, valid := podmanContainerFromInspect(reference, payload)
	if !valid {
		return unknown, ErrProtocol
	}

	return ContainerProbe{State: ContainerProbeObserved, Container: container}, nil
}

func validContainerReference(value string) bool {
	return validContainerID(value) || validContainerName(value)
}

func validContainerID(value string) bool {
	if len(value) != containerIDHexBytes {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			if value[index] < 'a' || value[index] > 'f' {
				return false
			}
		}
	}

	return true
}

func validContainerName(value string) bool {
	if len(value) == 0 || len(value) > maximumContainerNameBytes || !alphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !alphaNumeric(value[index]) && value[index] != '.' && value[index] != '_' && value[index] != '-' {
			return false
		}
	}

	return true
}

func alphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func validPodmanStrings(values []string) bool {
	return !slices.ContainsFunc(values, func(value string) bool { return !validPodmanOptionalText(value) })
}

func validPodmanOptionalText(value string) bool {
	return len(value) <= maximumTextBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func containerConfigurationMatches(container Container, workload domain.DesiredWorkload) bool {
	if container.Name != workload.ContainerName || container.ImageReference != workload.Image.Reference ||
		container.ImageConfig != workload.Image.ImageConfig ||
		container.PlatformManifest != workload.Image.PlatformManifest {
		return false
	}
	observed := canonicalPodmanSpec(container.WorkloadSpec)
	expected := canonicalPodmanSpec(workload.WorkloadSpec)
	observed.ServiceName = expected.ServiceName
	observed.Platform = expected.Platform

	return reflect.DeepEqual(observed, expected)
}

func containerConfigurationDigest(container Container) domain.Digest {
	evidence := []byte{podmanConfigurationVersion}
	evidence = appendPodmanString(evidence, container.ImageReference)
	evidence = append(evidence, container.ImageConfig[:]...)
	evidence = append(evidence, container.PlatformManifest[:]...)
	configuration := domain.ComputeWorkloadSpecDigest(canonicalPodmanSpec(container.WorkloadSpec))
	evidence = append(evidence, configuration[:]...)

	return domain.Hash(evidence)
}

//nolint:cyclop // Canonicalization independently normalizes each optional Compose field.
func canonicalPodmanSpec(spec domain.WorkloadSpec) domain.WorkloadSpec {
	spec = spec.Clone()
	if spec.Cgroup == podmanCgroupPrivate {
		spec.Cgroup = ""
	}
	if spec.SharedMemoryBytes == podmanDefaultSharedMemory {
		spec.SharedMemoryBytes = 0
	}
	if spec.StopSignal == podmanSignalTerminateName || spec.StopSignal == "15" {
		spec.StopSignal = ""
	}
	if spec.StopTimeout != nil && *spec.StopTimeout == podmanDefaultStopTimeout {
		spec.StopTimeout = nil
	}
	if spec.OOMScoreAdj != nil && *spec.OOMScoreAdj == 0 {
		spec.OOMScoreAdj = nil
	}
	if spec.Init != nil && !*spec.Init {
		spec.Init = nil
	}
	if spec.StdinOpen != nil && !*spec.StdinOpen {
		spec.StdinOpen = nil
	}
	if spec.OOMKillDisable != nil && !*spec.OOMKillDisable {
		spec.OOMKillDisable = nil
	}
	if spec.ReadOnly != nil && !*spec.ReadOnly {
		spec.ReadOnly = nil
	}
	if spec.TTY != nil && !*spec.TTY {
		spec.TTY = nil
	}
	canonicalPodmanPorts(&spec)
	canonicalPodmanTmpfs(&spec)
	canonicalPodmanOrder(&spec)
	canonicalPodmanCollections(&spec)

	return spec
}

func canonicalPodmanPorts(spec *domain.WorkloadSpec) {
	bound := make(map[string]struct{}, len(spec.Ports))
	for index := range spec.Ports {
		if spec.Ports[index].HostIP == podmanHostAnyIPv4 {
			spec.Ports[index].HostIP = ""
		}
		key := strconv.FormatUint(uint64(spec.Ports[index].TargetPort), 10) + "/" + spec.Ports[index].Protocol
		bound[key] = struct{}{}
	}
	spec.ExposedPorts = slices.DeleteFunc(spec.ExposedPorts, func(value domain.ExposedPort) bool {
		key := strconv.FormatUint(uint64(value.TargetPort), 10) + "/" + value.Protocol
		_, published := bound[key]

		return published
	})
}

func canonicalPodmanTmpfs(spec *domain.WorkloadSpec) {
	for index := range spec.Tmpfs {
		spec.Tmpfs[index].Options = slices.DeleteFunc(spec.Tmpfs[index].Options, func(value string) bool {
			return value == podmanPropagationPrivate || value == "nosuid" || value == "nodev" ||
				value == "tmpcopyup"
		})
	}
}

func canonicalPodmanOrder(spec *domain.WorkloadSpec) {
	slices.Sort(spec.CapAdd)
	slices.Sort(spec.CapDrop)
	slices.Sort(spec.DNS)
	slices.Sort(spec.DNSOptions)
	slices.Sort(spec.DNSSearch)
	slices.Sort(spec.ExtraHosts)
	slices.Sort(spec.GroupAdd)
	slices.Sort(spec.Environment)
	slices.Sort(spec.Labels)
	slices.SortFunc(spec.Tmpfs, func(left, right domain.TmpfsMount) int {
		return strings.Compare(left.Target, right.Target)
	})
	slices.SortFunc(spec.Ulimits, func(left, right domain.Ulimit) int {
		return strings.Compare(left.Name, right.Name)
	})
	slices.SortFunc(spec.ExposedPorts, func(left, right domain.ExposedPort) int {
		if left.TargetPort != right.TargetPort {
			return int(left.TargetPort) - int(right.TargetPort)
		}

		return strings.Compare(left.Protocol, right.Protocol)
	})
	slices.SortFunc(spec.Ports, func(left, right domain.PortBinding) int {
		return strings.Compare(podmanPortSortKey(left), podmanPortSortKey(right))
	})
	slices.SortFunc(spec.Mounts, func(left, right domain.Mount) int {
		return strings.Compare(left.Target+"\x00"+left.Source, right.Target+"\x00"+right.Source)
	})
}

//nolint:cyclop // Native inspect omits every empty collection independently.
func canonicalPodmanCollections(spec *domain.WorkloadSpec) {
	if len(spec.CapAdd) == 0 {
		spec.CapAdd = nil
	}
	if len(spec.CapDrop) == 0 {
		spec.CapDrop = nil
	}
	if len(spec.DNS) == 0 {
		spec.DNS = nil
	}
	if len(spec.DNSOptions) == 0 {
		spec.DNSOptions = nil
	}
	if len(spec.DNSSearch) == 0 {
		spec.DNSSearch = nil
	}
	if len(spec.ExtraHosts) == 0 {
		spec.ExtraHosts = nil
	}
	if len(spec.GroupAdd) == 0 {
		spec.GroupAdd = nil
	}
	if len(spec.Tmpfs) == 0 {
		spec.Tmpfs = nil
	}
	if len(spec.Ulimits) == 0 {
		spec.Ulimits = nil
	}
	if len(spec.Environment) == 0 {
		spec.Environment = nil
	}
	if len(spec.Labels) == 0 {
		spec.Labels = nil
	}
	if len(spec.ExposedPorts) == 0 {
		spec.ExposedPorts = nil
	}
	if len(spec.Ports) == 0 {
		spec.Ports = nil
	}
	if len(spec.Mounts) == 0 {
		spec.Mounts = nil
	}
}

//nolint:cyclop // Every ownership label is required and verified independently.
func decodeOwnership(
	labels map[string]string,
	imageConfig domain.Digest,
	platformManifest domain.Digest,
) domain.WorkloadOwnership {
	if !hasManiudLabel(labels) {
		return domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged}
	}
	if !supportedOwnershipLabels(labels) {
		return domain.WorkloadOwnership{}
	}
	desired, desiredErr := domain.ParseDigest(labels[domain.LabelDesiredStateDigest])
	reference, referenceErr := domain.ParseDigest(labels[domain.LabelReferenceDigest])
	labeledImage, imageErr := domain.ParseDigest(labels[domain.LabelImageConfigDigest])
	labeledManifest, manifestErr := domain.ParseDigest(labels[domain.LabelPlatformManifestDigest])
	service := labels[domain.LabelService]
	transaction := labels[domain.LabelTransaction]
	if desiredErr != nil || referenceErr != nil || imageErr != nil || manifestErr != nil ||
		labeledImage != imageConfig || labeledManifest != platformManifest ||
		!validOwnershipName(service) || !validOwnershipName(transaction) {
		return domain.WorkloadOwnership{}
	}

	return domain.WorkloadOwnership{
		Status: domain.OwnershipManaged, Service: service, Transaction: transaction,
		DesiredState: desired, Reference: reference,
		ImageConfig: labeledImage, PlatformManifest: labeledManifest,
	}
}

func hasManiudLabel(labels map[string]string) bool {
	for key := range labels {
		if strings.HasPrefix(key, maniudLabelPrefix) {
			return true
		}
	}

	return false
}

func supportedOwnershipLabels(labels map[string]string) bool {
	required := map[string]bool{
		domain.LabelService: false, domain.LabelTransaction: false,
		domain.LabelDesiredStateDigest: false, domain.LabelReferenceDigest: false,
		domain.LabelImageConfigDigest: false, domain.LabelPlatformManifestDigest: false,
	}
	for key := range labels {
		if _, found := required[key]; found {
			required[key] = true

			continue
		}
		if strings.HasPrefix(key, maniudLabelPrefix) {
			return false
		}
	}
	for _, present := range required {
		if !present {
			return false
		}
	}

	return true
}

func validOwnershipName(value string) bool {
	if len(value) == 0 || len(value) > maximumOwnershipValueBytes || !alphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !alphaNumeric(value[index]) && value[index] != '.' && value[index] != '_' && value[index] != '-' {
			return false
		}
	}

	return true
}

func workloadOwnershipLabels(workload domain.DesiredWorkload, transaction string) map[string]string {
	return map[string]string{
		domain.LabelService:                workload.ServiceName,
		domain.LabelTransaction:            transaction,
		domain.LabelDesiredStateDigest:     workload.EffectiveDigest.String(),
		domain.LabelReferenceDigest:        workload.Image.ReferenceDigest.String(),
		domain.LabelImageConfigDigest:      workload.Image.ImageConfig.String(),
		domain.LabelPlatformManifestDigest: workload.Image.PlatformManifest.String(),
	}
}

func podmanPortSortKey(value domain.PortBinding) string {
	return value.Protocol + "\x00" + strconv.FormatUint(uint64(value.TargetPort), 10) + "\x00" +
		value.HostIP + "\x00" + strconv.FormatUint(uint64(value.PublishedPort), 10)
}
