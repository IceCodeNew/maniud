package podman

import (
	"fmt"
	"slices"
	"strings"

	podmanconfig "github.com/IceCodeNew/maniud/containerconfig/podman"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

func parseExpectedImageReference(image domain.ImageIdentity) (imageref.Reference, error) {
	reference, err := imageref.Parse(image.Reference)
	if err != nil {
		return imageref.Reference{}, fmt.Errorf("parse podman image reference: %w", err)
	}

	return reference, nil
}

func encodePodmanWorkload(
	workload domain.DesiredWorkload,
	transaction string,
	options application.WorkloadCreateOptions,
) ([]byte, bool) {
	spec, valid := podmanOwnedSpec(workload, transaction)
	if !valid {
		return nil, false
	}
	document, err := podmanconfig.Encode(spec, podmanconfig.CreateOptions{
		ImageReference: workload.Image.Reference, CopyImageVolumes: options.CopyImageVolumes,
	})

	return document, err == nil && len(document) <= int(maximumControlBytes)
}

func podmanOwnedSpec(workload domain.DesiredWorkload, transaction string) (domain.WorkloadSpec, bool) {
	spec := workload.Clone()
	for _, label := range spec.Labels {
		key, _, _ := strings.Cut(label, "=")
		if domain.IsOwnershipLabel(key) || strings.HasPrefix(key, maniudLabelPrefix) {
			return domain.WorkloadSpec{}, false
		}
	}
	for key, value := range workloadOwnershipLabels(workload, transaction) {
		spec.Labels = append(spec.Labels, key+"="+value)
	}

	return spec, true
}

func podmanContainerFromInspection(reference string, inspection podmanconfig.Inspection) (Container, bool) {
	var empty Container
	if reference != inspection.ID && reference != inspection.Name ||
		inspection.ImageReference == "" || !validPodmanText(inspection.ImageReference) {
		return empty, false
	}
	imageConfig, imageValid := podmanImageID(inspection.ImageID)
	imageReference, referenceErr := imageref.Parse(inspection.ImageReference)
	observedDigest, digestErr := domain.ParseDigest(inspection.ImageDigest)
	if !imageValid || referenceErr != nil || digestErr != nil {
		return empty, false
	}
	ownership := decodeOwnership(
		inspection.RawLabels,
		imageConfig,
		imageReference.Digest(),
		observedDigest,
	)
	platformManifest := observedDigest
	if ownership.Status == domain.OwnershipManaged {
		platformManifest = ownership.PlatformManifest
	}
	spec := inspection.Spec.Clone()
	spec.Labels = slices.DeleteFunc(spec.Labels, func(label string) bool {
		key, _, _ := strings.Cut(label, "=")

		return domain.IsOwnershipLabel(key)
	})
	if len(spec.Labels) == 0 {
		spec.Labels = nil
	}

	return Container{
		ID: inspection.ID, Name: inspection.Name, ImageReference: inspection.ImageReference,
		ImageConfig: imageConfig, PlatformManifest: platformManifest,
		WorkloadSpec: spec, RuntimeMounts: podmanRuntimeMounts(inspection.RuntimeMounts),
		State: inspection.State, Ownership: ownership,
	}, true
}

func podmanRuntimeMounts(values []podmanconfig.RuntimeMount) []domain.RuntimeMount {
	if values == nil {
		return nil
	}
	result := make([]domain.RuntimeMount, len(values))
	for index, value := range values {
		result[index] = domain.RuntimeMount{
			Kind: value.Kind, Name: value.Name, Source: value.Source,
			Target: value.Target, ReadOnly: value.ReadOnly,
		}
	}

	return result
}
