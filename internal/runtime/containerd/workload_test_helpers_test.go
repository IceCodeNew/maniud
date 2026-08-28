package containerd

import (
	"testing"

	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	versionapi "github.com/containerd/containerd/api/services/version/v1"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testWorkloadTransaction = "transaction-1"
	testWorkloadService     = "api"
	testWorkloadName        = "api-1"
	testRestartPolicy       = "always"
	testRelativePath        = "relative"
	testSourcePath          = "/source"
	testChangedValue        = "changed"
	testBadValue            = "bad"
	testDNSAddress          = "1.1.1.1"
	testDataMount           = "/data"
	testStateMount          = "/state"
	testFileName            = "file"
	testOtherValue          = "other"
	testContainerValue      = "container"
	testTaskLifecycleCase   = "task lifecycle"
	testHostUnavailableCase = "host unavailable"
	testImageValue          = "image"
	testBadIdentifier       = "bad/id"
	testWrongPath           = "/wrong"
	testRenamedWorkloadName = "api-old"
	testMissingPath         = "/missing"
	testMissingValue        = "missing-value"
	testNullDevice          = "/dev/null"
)

func testContainerdDesiredWorkload(t *testing.T) domain.DesiredWorkload {
	t.Helper()

	reference := domain.Hash([]byte("reference"))
	platform := domain.Platform{OS: containerdPlatformOS, Architecture: containerdArchitectureAMD64}
	workload := domain.DesiredWorkload{
		ServiceName:   testWorkloadService,
		ContainerName: testWorkloadName,
		Platform:      platform,
		Entrypoint:    []string{"/bin/true"},
		NetworkMode:   defaultCNINetworkName,
		Image: domain.ImageIdentity{
			Origin:           domain.ImageOriginRegistry,
			Reference:        "example.com/team/api@" + reference.String(),
			ReferenceDigest:  reference,
			Platform:         platform,
			PlatformManifest: domain.Hash([]byte("manifest")),
			ImageConfig:      domain.Hash([]byte("config")),
			Entrypoint:       []string{"/bin/true"},
		},
		SourceDigest: domain.Hash([]byte("source")),
	}
	workload.EffectiveDigest = domain.ComputeEffectiveDigest(workload)
	if err := containerdconfig.Validate(workload.WorkloadSpec); err != nil {
		t.Fatalf("test workload is invalid: %v", err)
	}

	return workload
}

func testContainerdConfiguration(t *testing.T, workload domain.DesiredWorkload) containerdconfig.Configuration {
	t.Helper()

	configuration, err := containerdconfig.Encode(workload.WorkloadSpec)
	if err != nil {
		t.Fatalf("encode test configuration: %v", err)
	}

	return configuration
}

func testManagedNativeWorkload(t *testing.T, desired domain.DesiredWorkload) *nativeWorkload {
	t.Helper()

	workload := &nativeWorkload{
		ID:                   workloadIdentifier(desired.ContainerName, testWorkloadTransaction),
		Name:                 desired.ContainerName,
		ImageReference:       desired.Image.Reference,
		ImageConfig:          desired.Image.ImageConfig,
		PlatformManifest:     desired.Image.PlatformManifest,
		Configuration:        testContainerdConfiguration(t, desired),
		Ports:                desired.Ports,
		RuntimeMounts:        nil,
		ConfigurationMatches: true,
		Lifecycle:            application.WorkloadLifecycleCreated,
		Ownership: domain.WorkloadOwnership{
			Status:           domain.OwnershipManaged,
			Service:          desired.ServiceName,
			Transaction:      testWorkloadTransaction,
			DesiredState:     desired.EffectiveDigest,
			Reference:        desired.Image.ReferenceDigest,
			ImageConfig:      desired.Image.ImageConfig,
			PlatformManifest: desired.Image.PlatformManifest,
		},
	}
	workload.ConfigurationDigest = containerdConfigurationDigest(*workload)

	return workload
}

func testCheckedWorkloadClient(t *testing.T, backend workloadBackend) *Client {
	t.Helper()

	versionValue := &versionapi.VersionResponse{Version: testContainerdVersion}
	server := &introspectionapi.ServerResponse{UUID: testContainerdServerUUID, Pid: 1, Pidns: 2}
	client := scopeTestClient(t, versionValue, server, server)
	client.workloads = backend

	return client
}
