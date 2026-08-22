package docker

import (
	"errors"
	"reflect"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	testImage        = "docker.io/example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testWorkloadName = "api"
)

func TestEncodeDecodePreservesPortableConfiguration(t *testing.T) {
	t.Parallel()

	spec := portableSpec()
	request, err := Encode(spec, CreateOptions{
		ImageReference: testImage, CopyImageVolumes: true,
	})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	observed, err := Decode(spec.ContainerName, request.Config, request.HostConfig)
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if observed.ServiceName != "" || observed.Platform != (containerconfig.Platform{}) {
		t.Fatalf("Decode() synthesized unavailable identity = %#v", observed)
	}
	if !Equivalent(observed, spec) {
		t.Fatalf("round trip = %#v, want %#v", observed, spec)
	}
	if !reflect.DeepEqual(observed.Labels, []string{"com.example.owner=team-a", "purpose=api"}) {
		t.Fatalf("Decode() labels = %#v", observed.Labels)
	}
	request.HostConfig.RestartPolicy = containertypes.RestartPolicy{}
	legacy, err := Decode(spec.ContainerName, request.Config, request.HostConfig)
	if err != nil || legacy.Restart != string(containertypes.RestartPolicyDisabled) {
		t.Fatalf("Decode(legacy restart) = %#v, %v", legacy.Restart, err)
	}
}

func TestPublicValidationAndCanonicalization(t *testing.T) {
	t.Parallel()

	spec := portableSpec()
	spec.Labels = []string{"z=last", "a=first"}
	if err := Validate(spec, CreateOptions{
		ImageReference: testImage, CopyImageVolumes: true,
	}); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	canonical, err := Canonical(spec)
	if err != nil {
		t.Fatalf("Canonical() error = %v", err)
	}
	if !reflect.DeepEqual(canonical.Labels, []string{"a=first", "z=last"}) {
		t.Fatalf("Canonical() labels = %#v", canonical.Labels)
	}

	invalid := spec.Clone()
	invalid.NetworkMode = "host"
	if _, err = Canonical(invalid); err == nil {
		t.Fatal("Canonical() accepted unsupported network mode")
	}
	if _, err = Decode(spec.ContainerName, nil, nil); err == nil {
		t.Fatal("Decode() accepted missing native configuration")
	} else {
		var validation containerconfig.ValidationError
		if !errors.As(err, &validation) || validation.Code != containerconfig.ValidationInvalidValue {
			t.Fatalf("Decode() error = %v", err)
		}
	}

	invalidProcess := spec.Clone()
	invalidProcess.Command = []string{"bad\x00command"}
	invalidSpecs := []containerconfig.Spec{
		spec.Clone(), spec.Clone(), spec.Clone(), spec.Clone(), spec.Clone(), invalidProcess,
	}
	invalidSpecs[0].Labels = []string{"a=1", "a=2"}
	invalidSpecs[1].ContainerName = ""
	invalidSpecs[2].ContainerName = "invalid_name"
	invalidSpecs[3].ServiceName = ""
	invalidSpecs[4].ServiceName = "invalid name"
	for index, invalidSpec := range invalidSpecs {
		if err := Validate(invalidSpec, CreateOptions{
			ImageReference: testImage, CopyImageVolumes: true,
		}); err == nil {
			t.Fatalf("Validate(invalid %d) accepted %#v", index, invalidSpec)
		}
	}
}

func portableSpec() containerconfig.Spec {
	truth := true

	return containerconfig.Spec{
		ServiceName: testWorkloadName, ContainerName: testWorkloadName,
		Platform:   containerconfig.Platform{OS: "linux", Architecture: "amd64"},
		Entrypoint: []string{"/init"}, Command: []string{"serve"}, NetworkMode: "bridge",
		Labels: []string{"purpose=api", "com.example.owner=team-a"},
		Init:   &truth,
	}
}
