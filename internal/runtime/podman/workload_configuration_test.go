package podman

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	podmanconfig "github.com/IceCodeNew/maniud/containerconfig/podman"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	podmanTestRoleKey   = "com.example.role"
	podmanTestRoleLabel = podmanTestRoleKey + "=api"
)

func testCreateOptions() application.WorkloadCreateOptions {
	return application.WorkloadCreateOptions{CopyImageVolumes: true}
}

func TestPodmanWorkloadEncodingAddsOnlyOwnedLabels(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	workload.Labels = []string{podmanTestRoleLabel}
	body, valid := encodePodmanWorkload(workload, podmanTestTransaction, application.WorkloadCreateOptions{})
	var document map[string]any
	if !valid || json.Unmarshal(body, &document) != nil {
		t.Fatalf("encodePodmanWorkload() = %s, %t", body, valid)
	}
	labels, ok := document["labels"].(map[string]any)
	if !ok || labels[podmanTestRoleKey] != "api" || labels[domain.LabelTransaction] != podmanTestTransaction {
		t.Fatalf("encoded labels = %#v", labels)
	}
	workload.Labels = []string{domain.LabelService + "=foreign"}
	body, ok = encodePodmanWorkload(
		workload, podmanTestTransaction, application.WorkloadCreateOptions{},
	)
	if ok || body != nil {
		t.Fatalf("encodePodmanWorkload(reserved) = %s, %t", body, ok)
	}
}

func podmanInspectionFixture(t *testing.T) (domain.DesiredWorkload, podmanconfig.Inspection) {
	t.Helper()
	workload := podmanTestWorkload(t)
	labels := workloadOwnershipLabels(workload, podmanTestTransaction)
	labels[podmanTestRoleKey] = "api"
	inspection := podmanconfig.Inspection{
		ID: podmanTestContainerID, Name: workload.ContainerName,
		ImageID: podmanImageConfig, ImageReference: workload.Image.Reference,
		ImageDigest: podmanManifestDigest, State: podmanconfig.StateRunning,
		Spec: workload.Clone(), RawLabels: labels,
		RuntimeMounts: []podmanconfig.RuntimeMount{{
			Kind: domain.MountVolume, Name: "volume", Source: "/data", Target: "/data",
		}},
	}
	for key, value := range labels {
		inspection.Spec.Labels = append(inspection.Spec.Labels, key+"="+value)
	}

	return workload, inspection
}

func TestPodmanInspectionBindsProjectEvidence(t *testing.T) {
	t.Parallel()

	workload, inspection := podmanInspectionFixture(t)
	container, valid := podmanContainerFromInspection(podmanTestContainerID, inspection)
	if !valid || container.State != ContainerRunning || container.Ownership.Status != domain.OwnershipManaged ||
		len(container.RuntimeMounts) != 1 || container.RuntimeMounts[0].Name != "volume" ||
		!reflect.DeepEqual(container.WorkloadSpec.Labels, []string{podmanTestRoleLabel}) {
		t.Fatalf("podmanContainerFromInspection() = %#v, %t", container, valid)
	}
	inspection.ImageDigest = podmanReferenceDigest
	container, valid = podmanContainerFromInspection(podmanTestContainerID, inspection)
	if !valid || container.PlatformManifest != workload.Image.PlatformManifest ||
		container.Ownership.Status != domain.OwnershipManaged {
		t.Fatalf("podmanContainerFromInspection(index digest) = %#v, %t", container, valid)
	}
}

func TestPodmanInspectionPreservesUnmanagedContainers(t *testing.T) {
	t.Parallel()

	workload, inspection := podmanInspectionFixture(t)
	inspection.RawLabels = nil
	inspection.Spec = workload.Clone()
	inspection.Spec.Labels = []string{podmanTestRoleLabel}
	container, valid := podmanContainerFromInspection(podmanTestContainerID, inspection)
	if !valid || container.PlatformManifest.String() != podmanManifestDigest ||
		container.Ownership.Status != domain.OwnershipUnmanaged {
		t.Fatalf("podmanContainerFromInspection(unmanaged) = %#v, %t", container, valid)
	}
}

func TestPodmanInspectionRejectsInvalidImageEvidence(t *testing.T) {
	t.Parallel()

	_, inspection := podmanInspectionFixture(t)
	inspection.ImageID = "invalid"
	if _, valid := podmanContainerFromInspection(podmanTestContainerID, inspection); valid {
		t.Fatal("podmanContainerFromInspection(invalid image) = true")
	}
}

func TestPodmanInspectionRejectsOversizedImageReference(t *testing.T) {
	t.Parallel()

	_, inspection := podmanInspectionFixture(t)
	inspection.ImageReference = strings.Repeat("x", maximumTextBytes+1)
	if _, valid := podmanContainerFromInspection(podmanTestContainerID, inspection); valid {
		t.Fatal("podmanContainerFromInspection(invalid reference) = true")
	}
}

func TestPodmanRuntimeMountsPreserveNilAndValues(t *testing.T) {
	t.Parallel()

	if podmanRuntimeMounts(nil) != nil {
		t.Fatal("podmanRuntimeMounts(nil) was non-nil")
	}
	got := podmanRuntimeMounts([]podmanconfig.RuntimeMount{{
		Kind: domain.MountBind, Source: "/source", Target: "/target", ReadOnly: true,
	}})
	if len(got) != 1 || got[0].Kind != domain.MountBind || !got[0].ReadOnly {
		t.Fatalf("podmanRuntimeMounts() = %#v", got)
	}
}

func TestPodmanConfigurationRejectsInvalidProjectInputs(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	invalidImage := workload.Image
	invalidImage.Reference = "bad\x00"
	if _, err := parseExpectedImageReference(invalidImage); err == nil {
		t.Fatal("parseExpectedImageReference(invalid) error = nil")
	}
	container := Container{
		Name: workload.ContainerName, ImageReference: workload.Image.Reference,
		ImageConfig: workload.Image.ImageConfig, PlatformManifest: workload.Image.PlatformManifest,
	}
	workload.ServiceName = ""
	if containerConfigurationMatches(container, workload) {
		t.Fatal("containerConfigurationMatches(invalid spec) = true")
	}
}

func TestPodmanConfigurationIgnoresInformationalImageTag(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	container := Container{
		Name:           workload.ContainerName,
		ImageReference: "registry.example/team/app@" + podmanReferenceDigest,
		ImageConfig:    workload.Image.ImageConfig, PlatformManifest: workload.Image.PlatformManifest,
		WorkloadSpec: workload.WorkloadSpec,
	}
	workload.Image.Reference = "registry.example/team/app:stable@" + podmanReferenceDigest
	if !containerConfigurationMatches(container, workload) {
		t.Fatal("containerConfigurationMatches(tag omitted by Podman) = false")
	}
	container.ImageReference = "registry.example/other/app@" + podmanReferenceDigest
	if containerConfigurationMatches(container, workload) {
		t.Fatal("containerConfigurationMatches(other repository) = true")
	}
}
