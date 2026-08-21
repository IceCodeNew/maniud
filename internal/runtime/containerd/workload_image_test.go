package containerd

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	api "github.com/containerd/containerd/api/types"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

type mappedContentClient map[string][]byte

func (client mappedContentClient) Info(
	_ context.Context,
	request *contentapi.InfoRequest,
	_ ...grpc.CallOption,
) (*contentapi.InfoResponse, error) {
	raw, found := client[request.GetDigest()]
	if !found {
		return nil, errContainerdTest
	}

	return &contentapi.InfoResponse{Info: &contentapi.Info{
		Digest: request.GetDigest(), Size: int64(len(raw)),
	}}, nil
}

type faultContentClient struct {
	mappedContentClient

	digest       string
	infoCalls    int
	readCalls    int
	infoResponse *contentapi.InfoResponse
	infoErr      error
	readData     []byte
	readErr      error
}

func (client *faultContentClient) Info(
	ctx context.Context,
	request *contentapi.InfoRequest,
	options ...grpc.CallOption,
) (*contentapi.InfoResponse, error) {
	if request.GetDigest() == client.digest {
		client.infoCalls++
		if client.infoCalls == 2 {
			return client.infoResponse, client.infoErr
		}
	}

	return client.mappedContentClient.Info(ctx, request, options...)
}

//nolint:ireturn // The generated gRPC client interface requires this stream type.
func (client *faultContentClient) Read(
	ctx context.Context,
	request *contentapi.ReadContentRequest,
	options ...grpc.CallOption,
) (contentapi.Content_ReadClient, error) {
	if request.GetDigest() == client.digest {
		client.readCalls++
		if client.readCalls == 2 {
			if client.readErr != nil {
				return nil, client.readErr
			}

			return &fakeContentStream{responses: []*contentapi.ReadContentResponse{{Data: client.readData}}}, nil
		}
	}

	return client.mappedContentClient.Read(ctx, request, options...)
}

//nolint:ireturn // The generated gRPC client interface requires this stream type.
func (client mappedContentClient) Read(
	_ context.Context,
	request *contentapi.ReadContentRequest,
	_ ...grpc.CallOption,
) (contentapi.Content_ReadClient, error) {
	raw, found := client[request.GetDigest()]
	if !found {
		return nil, errContainerdTest
	}

	return &fakeContentStream{responses: []*contentapi.ReadContentResponse{{Data: raw}}}, nil
}

//nolint:cyclop // The test compares the complete image identity for layered and scratch graphs.
func TestProbeImageAndSnapshotParentVerifyCompleteGraph(t *testing.T) {
	t.Parallel()

	for _, layers := range [][]byte{nil, []byte("layer")} {
		identity, image, content := testContainerdImageGraph(t, layers)
		client := testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
		client.images = fakeImagesClient{response: &imagesapi.GetImageResponse{Image: image}}
		client.content = content
		probe, err := client.ProbeImage(context.Background(), identity)
		if err != nil || probe.State != application.ImageProbeObserved ||
			probe.Image.ReferenceDigest != identity.ReferenceDigest ||
			probe.Image.PlatformManifest != identity.PlatformManifest ||
			probe.Image.ImageConfig != identity.ImageConfig {
			t.Fatalf("ProbeImage() = %#v, %v", probe, err)
		}
		parent, err := client.snapshotParent(context.Background(), identity)
		if err != nil {
			t.Fatalf("snapshotParent() = %q, %v", parent, err)
		}
		if len(layers) == 0 && parent != "" ||
			len(layers) != 0 && parent != digest.FromBytes(layers).String() {
			t.Fatalf("snapshotParent(%d layers) = %q", len(layers), parent)
		}
	}

	if _, err := (&Client{}).ProbeImage(
		context.Background(), domain.ImageIdentity{},
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeImage(invalid) = %v", err)
	}
}

//nolint:cyclop,funlen // Each case corrupts one independently verified image-graph boundary.
func TestProbeImageAndSnapshotParentFailureMatrix(t *testing.T) {
	t.Parallel()

	identity, image, content := testContainerdImageGraph(t, []byte("layer"))
	client := testCheckedWorkloadClient(t, &fakeWorkloadBackend{})
	client.content = content
	client.images = fakeImagesClient{err: status.Error(codes.NotFound, "missing")}
	probe, err := client.ProbeImage(context.Background(), identity)
	if err != nil || probe.State != application.ImageProbeMissing {
		t.Fatalf("ProbeImage(missing) = %#v, %v", probe, err)
	}
	client.images = fakeImagesClient{err: errContainerdTest}
	if _, err = client.ProbeImage(context.Background(), identity); err == nil {
		t.Fatal("ProbeImage(error) succeeded")
	}
	if _, err = client.snapshotParent(context.Background(), identity); err == nil {
		t.Fatal("snapshotParent(probe error) succeeded")
	}
	if _, err = client.snapshotParent(context.Background(), domain.ImageIdentity{
		Reference: "bad reference",
	}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("snapshotParent(reference) = %v", err)
	}

	client.images = fakeImagesClient{response: &imagesapi.GetImageResponse{Image: image}}
	client.content = content
	mismatch := identity
	mismatch.ImageConfig = domain.Hash([]byte("other configuration"))
	if _, err = client.snapshotParent(context.Background(), mismatch); !errors.Is(err, ErrProtocol) {
		t.Fatalf("snapshotParent(identity mismatch) = %v", err)
	}
	configurationDigest := identity.ImageConfig.String()
	tests := []struct {
		name         string
		infoResponse *contentapi.InfoResponse
		infoErr      error
		readData     []byte
		readErr      error
	}{
		{name: "info error", infoErr: errContainerdTest},
		{name: "missing info", infoResponse: &contentapi.InfoResponse{}},
		{
			name: "read error",
			infoResponse: &contentapi.InfoResponse{Info: &contentapi.Info{
				Digest: configurationDigest, Size: int64(len(content[configurationDigest])),
			}},
			readErr: errContainerdTest,
		},
		{
			name: "malformed configuration",
			infoResponse: &contentapi.InfoResponse{Info: &contentapi.Info{
				Digest: configurationDigest, Size: int64(len([]byte("invalid"))),
			}},
			readData: []byte("invalid"),
		},
	}
	for _, test := range tests {
		fault := &faultContentClient{
			mappedContentClient: content, digest: configurationDigest,
			infoResponse: test.infoResponse, infoErr: test.infoErr,
			readData: test.readData, readErr: test.readErr,
		}
		client.content = fault
		if _, err = client.snapshotParent(context.Background(), identity); err == nil {
			t.Fatalf("snapshotParent(%s) succeeded", test.name)
		}
	}

	if _, err = snapshotChainID([]byte("invalid")); !errors.Is(err, ErrProtocol) {
		t.Fatalf("snapshotChainID(malformed) = %v", err)
	}
	configuration := ocispec.Image{}
	configuration.RootFS.DiffIDs = []digest.Digest{"invalid"}
	raw, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = snapshotChainID(raw); !errors.Is(err, ErrProtocol) {
		t.Fatalf("snapshotChainID(invalid diff ID) = %v", err)
	}
}

func testContainerdImageGraph(
	t *testing.T,
	layers []byte,
) (domain.ImageIdentity, *imagesapi.Image, mappedContentClient) {
	t.Helper()

	platform := domain.Platform{OS: containerdPlatformOS, Architecture: containerdArchitectureAMD64}
	configuration := ocispec.Image{
		Platform: ocispec.Platform{OS: platform.OS, Architecture: platform.Architecture},
		RootFS:   ocispec.RootFS{Type: "layers"},
	}
	manifest := ocispec.Manifest{Versioned: ocispec.Manifest{}.Versioned}
	manifest.SchemaVersion = 2
	content := mappedContentClient{}
	if len(layers) != 0 {
		layerDigest := digest.FromBytes(layers)
		configuration.RootFS.DiffIDs = []digest.Digest{layerDigest}
		manifest.Layers = []ocispec.Descriptor{{
			MediaType: ocispec.MediaTypeImageLayer, Digest: layerDigest, Size: int64(len(layers)),
		}}
		content[layerDigest.String()] = layers
	}
	configurationRaw, err := json.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	configurationDigest := digest.FromBytes(configurationRaw)
	content[configurationDigest.String()] = configurationRaw
	manifest.Config = ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageConfig,
		Digest:    configurationDigest, Size: int64(len(configurationRaw)),
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestDigest := digest.FromBytes(manifestRaw)
	content[manifestDigest.String()] = manifestRaw
	reference := "example.com/team/api@" + manifestDigest.String()
	manifestIdentity, err := domain.ParseDigest(manifestDigest.String())
	if err != nil {
		t.Fatal(err)
	}
	configurationIdentity, err := domain.ParseDigest(configurationDigest.String())
	if err != nil {
		t.Fatal(err)
	}
	identity := domain.ImageIdentity{
		Origin: domain.ImageOriginRegistry, Reference: reference,
		ReferenceDigest: manifestIdentity,
		Platform:        platform, PlatformManifest: manifestIdentity,
		ImageConfig: configurationIdentity,
	}

	return identity, &imagesapi.Image{
		Name: reference,
		Target: &api.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    manifestDigest.String(), Size: int64(len(manifestRaw)),
		},
	}, content
}
