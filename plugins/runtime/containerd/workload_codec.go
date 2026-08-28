package containerd

import (
	"bytes"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	tasktypes "github.com/containerd/containerd/api/types/task"
	"google.golang.org/protobuf/types/known/anypb"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	maniudLabelPrefix                     = "io.maniud."
	containerNameLabel                    = "io.maniud.container-name"
	containerConfigurationExtension       = "io.maniud.container-configuration.v1"
	containerConfigurationTypeURL         = "io.maniud.container-configuration.v1"
	containerRuntimeSpecTypeURL           = "types.containerd.io/opencontainers/runtime-spec/1/Spec"
	containerExtensionVersion             = 1
	maximumContainerExtensionBytes        = 1 << 20
	containerdRestartStatusLabel          = "containerd.io/restart.status"
	containerdRestartPolicyLabel          = "containerd.io/restart.policy"
	containerdRestartCountLabel           = "containerd.io/restart.count"
	containerdExplicitlyStoppedLabel      = "containerd.io/restart.explicitly-stopped"
	containerdRestartDesiredRunning       = "running"
	containerdRestartDesiredStopped       = "stopped"
	containerdRestartExplicitlyStopped    = "true"
	containerdRestartNotExplicitlyStopped = "false"
)

type workloadExtensionV1 struct {
	Version           int                            `json:"version"`
	Configuration     containerdconfig.Configuration `json:"configuration"`
	ImageReference    string                         `json:"image_reference"`
	ImageConfig       string                         `json:"image_config"`
	PlatformManifest  string                         `json:"platform_manifest"`
	RuntimeMounts     []domain.RuntimeMount          `json:"runtime_mounts"`
	RuntimeSpecDigest string                         `json:"runtime_spec_digest"`
	SnapshotParent    string                         `json:"snapshot_parent"`
	NetworkDigest     string                         `json:"network_digest"`
}

func encodeWorkloadExtension(extension workloadExtensionV1) (*anypb.Any, error) {
	//nolint:musttag // workloadExtensionV1 and every nested projection define explicit JSON tags.
	raw, err := json.Marshal(extension)
	if err != nil || len(raw) == 0 || len(raw) > maximumContainerExtensionBytes {
		return nil, ErrProtocol
	}

	return &anypb.Any{TypeUrl: containerConfigurationTypeURL, Value: raw}, nil
}

func decodeWorkloadExtension(value *anypb.Any) (workloadExtensionV1, error) {
	var extension workloadExtensionV1
	if value == nil || value.GetTypeUrl() != containerConfigurationTypeURL ||
		len(value.GetValue()) == 0 || len(value.GetValue()) > maximumContainerExtensionBytes {
		return extension, ErrProtocol
	}
	decoder := json.NewDecoder(bytes.NewReader(value.GetValue()))
	decoder.DisallowUnknownFields()
	//nolint:musttag // workloadExtensionV1 and every nested projection define explicit JSON tags.
	if decoder.Decode(&extension) != nil || decoder.Decode(&struct{}{}) == nil ||
		extension.Version != containerExtensionVersion {
		return workloadExtensionV1{}, ErrProtocol
	}
	//nolint:musttag // workloadExtensionV1 and every nested projection define explicit JSON tags.
	encoded, err := json.Marshal(extension)
	if err != nil || !bytes.Equal(encoded, value.GetValue()) {
		return workloadExtensionV1{}, ErrProtocol
	}

	return extension, nil
}

func encodeRuntimeSpec(configuration containerdconfig.Configuration) (*anypb.Any, domain.Digest, error) {
	raw, err := json.Marshal(configuration.OCI)
	if err != nil || len(raw) == 0 || len(raw) > maximumContainerExtensionBytes {
		return nil, domain.Digest{}, ErrProtocol
	}
	digest := domain.Hash(raw)

	return &anypb.Any{TypeUrl: containerRuntimeSpecTypeURL, Value: raw}, digest, nil
}

func runtimeSpecDigest(value *anypb.Any) (domain.Digest, error) {
	if value == nil || value.GetTypeUrl() != containerRuntimeSpecTypeURL ||
		len(value.GetValue()) == 0 || len(value.GetValue()) > maximumContainerExtensionBytes ||
		!json.Valid(value.GetValue()) {
		return domain.Digest{}, ErrProtocol
	}

	return domain.Hash(value.GetValue()), nil
}

func workloadLabels(workload domain.DesiredWorkload, transaction string) map[string]string {
	return map[string]string{
		containerNameLabel:                 workload.ContainerName,
		domain.LabelService:                workload.ServiceName,
		domain.LabelTransaction:            transaction,
		domain.LabelDesiredStateDigest:     workload.EffectiveDigest.String(),
		domain.LabelReferenceDigest:        workload.Image.ReferenceDigest.String(),
		domain.LabelImageConfigDigest:      workload.Image.ImageConfig.String(),
		domain.LabelPlatformManifestDigest: workload.Image.PlatformManifest.String(),
	}
}

func restartLabels(restart string) map[string]string {
	if restart == "" || restart == "no" {
		return nil
	}

	return map[string]string{
		containerdRestartStatusLabel:     containerdRestartDesiredStopped,
		containerdRestartPolicyLabel:     restart,
		containerdRestartCountLabel:      "0",
		containerdExplicitlyStoppedLabel: containerdRestartNotExplicitlyStopped,
	}
}

//nolint:cyclop // Ownership parsing treats partial and malformed label sets as conflicting.
func parseWorkloadOwnership(labels map[string]string) domain.WorkloadOwnership {
	keys := []string{
		domain.LabelService,
		domain.LabelTransaction,
		domain.LabelDesiredStateDigest,
		domain.LabelReferenceDigest,
		domain.LabelImageConfigDigest,
		domain.LabelPlatformManifestDigest,
	}
	present := 0
	for _, key := range keys {
		if _, found := labels[key]; found {
			present++
		}
	}
	if present == 0 {
		if slices.ContainsFunc(slices.Collect(maps.Keys(labels)), func(key string) bool {
			return strings.HasPrefix(key, maniudLabelPrefix)
		}) {
			return domain.WorkloadOwnership{Status: domain.OwnershipConflicting}
		}

		return domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged}
	}
	if present != len(keys) {
		return domain.WorkloadOwnership{Status: domain.OwnershipConflicting}
	}
	desired, desiredErr := domain.ParseDigest(labels[domain.LabelDesiredStateDigest])
	reference, referenceErr := domain.ParseDigest(labels[domain.LabelReferenceDigest])
	imageConfig, imageConfigErr := domain.ParseDigest(labels[domain.LabelImageConfigDigest])
	manifest, manifestErr := domain.ParseDigest(labels[domain.LabelPlatformManifestDigest])
	if labels[domain.LabelService] == "" || labels[domain.LabelTransaction] == "" ||
		desiredErr != nil || referenceErr != nil || imageConfigErr != nil || manifestErr != nil {
		return domain.WorkloadOwnership{Status: domain.OwnershipConflicting}
	}

	return domain.WorkloadOwnership{
		Status: domain.OwnershipManaged, Service: labels[domain.LabelService],
		Transaction: labels[domain.LabelTransaction], DesiredState: desired,
		Reference: reference, ImageConfig: imageConfig, PlatformManifest: manifest,
	}
}

func taskLifecycle(status tasktypes.Status, found bool) (application.WorkloadLifecycle, error) {
	if !found {
		return application.WorkloadLifecycleCreated, nil
	}
	switch status {
	case tasktypes.Status_CREATED:
		return application.WorkloadLifecycleCreated, nil
	case tasktypes.Status_RUNNING:
		return application.WorkloadLifecycleRunning, nil
	case tasktypes.Status_STOPPED:
		return application.WorkloadLifecycleExited, nil
	case tasktypes.Status_PAUSED:
		return application.WorkloadLifecyclePaused, nil
	case tasktypes.Status_UNKNOWN, tasktypes.Status_PAUSING:
	}

	return application.WorkloadLifecycleUnknown, ErrProtocol
}

func cloneStringLabels(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	maps.Copy(clone, source)

	return clone
}
