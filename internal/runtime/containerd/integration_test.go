//go:build linux

package containerd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	versionapi "github.com/containerd/containerd/api/services/version/v1"
	containertypes "github.com/containerd/containerd/api/types"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
)

const (
	testContainerdNamespace  = "maniud-test"
	testContainerdImage      = "example.com/team/api:1"
	testContainerdScopeText  = "test"
	testContainerdServerUUID = "server"
	testContainerdVersion    = "2.3.4"
	testMediaTypeField       = "mediaType"
)

type testContainerdState struct {
	mutex sync.Mutex

	image        *imagesapi.Image
	blobs        map[string][]byte
	reads        []string
	imageStatus  codes.Code
	missing      string
	corrupt      string
	offset       string
	truncate     string
	infoConflict string
	scopeCalls   int
	driftAfter   int
	uuid         string
}

type testImagesServer struct {
	imagesapi.UnimplementedImagesServer

	state *testContainerdState
}

type testContentServer struct {
	contentapi.UnimplementedContentServer

	state *testContainerdState
}

type testIntrospectionServer struct {
	introspectionapi.UnimplementedIntrospectionServer

	state *testContainerdState
}

type testVersionServer struct {
	versionapi.UnimplementedVersionServer
}

func (server *testImagesServer) Get(
	ctx context.Context,
	request *imagesapi.GetImageRequest,
) (*imagesapi.GetImageResponse, error) {
	if err := requireTestNamespace(ctx); err != nil {
		return nil, err
	}
	server.state.mutex.Lock()
	defer server.state.mutex.Unlock()

	if server.state.imageStatus != codes.OK {
		return nil, fmt.Errorf("image fixture status: %w", status.Error(server.state.imageStatus, "image failure"))
	}
	if request.GetName() != testContainerdImage {
		return nil, fmt.Errorf("image fixture lookup: %w", status.Error(codes.NotFound, "missing image"))
	}

	return &imagesapi.GetImageResponse{Image: server.state.image}, nil
}

func (server *testContentServer) Info(
	ctx context.Context,
	request *contentapi.InfoRequest,
) (*contentapi.InfoResponse, error) {
	if err := requireTestNamespace(ctx); err != nil {
		return nil, err
	}
	server.state.mutex.Lock()
	defer server.state.mutex.Unlock()

	raw, found := server.state.blobs[request.GetDigest()]
	if !found || request.GetDigest() == server.state.missing {
		return nil, fmt.Errorf("content fixture lookup: %w", status.Error(codes.NotFound, "missing content"))
	}
	digestValue := request.GetDigest()
	if digestValue == server.state.infoConflict {
		digestValue = domain.Hash([]byte("other")).String()
	}

	return &contentapi.InfoResponse{Info: &contentapi.Info{
		Digest: digestValue, Size: int64(len(raw)),
	}}, nil
}

func (server *testContentServer) Read(
	request *contentapi.ReadContentRequest,
	stream contentapi.Content_ReadServer,
) error {
	raw, corrupt, offset, truncate, err := server.contentForRead(stream.Context(), request)
	if err != nil {
		return err
	}

	return sendTestContent(stream, raw, corrupt, offset, truncate)
}

func (server *testContentServer) contentForRead(
	ctx context.Context,
	request *contentapi.ReadContentRequest,
) ([]byte, bool, bool, bool, error) {
	if err := requireTestNamespace(ctx); err != nil {
		return nil, false, false, false, err
	}
	server.state.mutex.Lock()
	raw, found := server.state.blobs[request.GetDigest()]
	corrupt := request.GetDigest() == server.state.corrupt
	offset := request.GetDigest() == server.state.offset
	truncate := request.GetDigest() == server.state.truncate
	server.state.reads = append(server.state.reads, request.GetDigest())
	server.state.mutex.Unlock()
	if !found || request.GetSize() != int64(len(raw)) || request.GetOffset() != 0 {
		return nil, false, false, false, fmt.Errorf(
			"content fixture read: %w",
			status.Error(codes.NotFound, "missing content"),
		)
	}

	return raw, corrupt, offset, truncate, nil
}

func sendTestContent(
	stream contentapi.Content_ReadServer,
	raw []byte,
	corrupt bool,
	offset bool,
	truncate bool,
) error {
	if corrupt {
		raw = append([]byte(nil), raw...)
		raw[0] ^= 1
	}
	if truncate {
		raw = raw[:len(raw)-1]
	}
	middle := len(raw) / 2
	if middle == 0 {
		middle = len(raw)
	}
	firstOffset := int64(0)
	if offset {
		firstOffset = 1
	}
	if err := stream.Send(&contentapi.ReadContentResponse{Offset: firstOffset, Data: raw[:middle]}); err != nil {
		return fmt.Errorf("send first content fixture chunk: %w", err)
	}
	if middle < len(raw) {
		if err := stream.Send(&contentapi.ReadContentResponse{Offset: int64(middle), Data: raw[middle:]}); err != nil {
			return fmt.Errorf("send final content fixture chunk: %w", err)
		}
	}

	return nil
}

func (server *testIntrospectionServer) Server(
	ctx context.Context,
	_ *emptypb.Empty,
) (*introspectionapi.ServerResponse, error) {
	if err := requireTestNamespace(ctx); err != nil {
		return nil, err
	}
	server.state.mutex.Lock()
	defer server.state.mutex.Unlock()

	server.state.scopeCalls++
	uuid := server.state.uuid
	if server.state.driftAfter > 0 && server.state.scopeCalls > server.state.driftAfter {
		uuid += "-changed"
	}

	return &introspectionapi.ServerResponse{UUID: uuid, Pid: 42, Pidns: 84}, nil
}

func (*testVersionServer) Version(
	ctx context.Context,
	_ *emptypb.Empty,
) (*versionapi.VersionResponse, error) {
	if err := requireTestNamespace(ctx); err != nil {
		return nil, err
	}

	return &versionapi.VersionResponse{Version: testContainerdVersion, Revision: testContainerdScopeText}, nil
}

func requireTestNamespace(ctx context.Context) error {
	values, valid := metadata.FromIncomingContext(ctx)
	if !valid || !slices.Equal(values.Get(containerdNamespaceHeader), []string{testContainerdNamespace}) {
		return fmt.Errorf("namespace fixture: %w", status.Error(codes.Unauthenticated, "invalid namespace"))
	}

	return nil
}

func TestConnectResolvesAndVerifiesCompleteLocalImage(t *testing.T) {
	t.Parallel()

	state, source, platform, layerDigest := newTestContainerdState(t)
	path, stop := startTestContainerd(t, state)
	t.Cleanup(stop)
	identity, err := ResolveLocalImage(
		context.Background(), "unix://"+path, testContainerdNamespace, source, platform,
	)
	if err != nil {
		t.Fatalf("ResolveLocalImage() error = %v", err)
	}
	if identity.Reference != testContainerdImage+"@"+state.image.GetTarget().GetDigest() ||
		identity.ReferenceDigest.String() != state.image.GetTarget().GetDigest() || identity.Platform != platform ||
		!slices.Equal(identity.Entrypoint, []string{"/init"}) || !slices.Equal(identity.Command, []string{"serve"}) {
		t.Fatalf("ResolveLocalImage() = %#v", identity)
	}
	state.mutex.Lock()
	layerRead := slices.Contains(state.reads, layerDigest)
	state.mutex.Unlock()
	if !layerRead {
		t.Fatal("ResolveLocalImage() did not verify the selected image layer")
	}
}

func TestResolveLocalImageContainsConnectionFailures(t *testing.T) {
	t.Parallel()

	_, err := ResolveLocalImage(
		context.Background(), "relative", testContainerdNamespace, imageref.Source{}, domain.Platform{},
	)
	if !errors.Is(err, registry.ErrProtocol) {
		t.Fatalf("ResolveLocalImage(invalid endpoint) = %v", err)
	}
}

func TestContainerdResolveFailsClosed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		configure func(*testContainerdState, string)
		want      error
	}{
		{
			name:      "image missing",
			configure: func(state *testContainerdState, _ string) { state.imageStatus = codes.NotFound },
			want:      registry.ErrNotFound,
		},
		{
			name:      "layer missing",
			configure: func(state *testContainerdState, layer string) { state.missing = layer },
			want:      registry.ErrNotFound,
		},
		{
			name:      "layer digest conflict",
			configure: func(state *testContainerdState, layer string) { state.corrupt = layer },
			want:      registry.ErrProtocol,
		},
		{
			name:      "layer offset conflict",
			configure: func(state *testContainerdState, layer string) { state.offset = layer },
			want:      registry.ErrProtocol,
		},
		{
			name:      "layer truncated",
			configure: func(state *testContainerdState, layer string) { state.truncate = layer },
			want:      registry.ErrProtocol,
		},
		{
			name:      "content info conflict",
			configure: func(state *testContainerdState, layer string) { state.infoConflict = layer },
			want:      registry.ErrProtocol,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, source, platform, layer := newTestContainerdState(t)
			test.configure(state, layer)
			client := connectTestContainerd(t, state)
			if _, err := client.Resolve(context.Background(), source, platform); !errors.Is(err, test.want) {
				t.Fatalf("Resolve() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestContainerdResolveRejectsScopeDrift(t *testing.T) {
	t.Parallel()

	state, source, platform, _ := newTestContainerdState(t)
	client := connectTestContainerd(t, state)
	state.mutex.Lock()
	state.driftAfter = state.scopeCalls
	state.mutex.Unlock()
	if _, err := client.Resolve(context.Background(), source, platform); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestConnectRejectsIncompleteScope(t *testing.T) {
	t.Parallel()

	state, _, _, layer := newTestContainerdState(t)
	_ = layer
	state.uuid = ""
	path, stop := startTestContainerd(t, state)
	defer stop()
	client, err := Connect(context.Background(), path, testContainerdNamespace)
	if client != nil || !errors.Is(err, ErrProtocol) {
		t.Fatalf("Connect() = %#v, %v", client, err)
	}
}

func newTestContainerdState(
	t *testing.T,
) (*testContainerdState, imageref.Source, domain.Platform, string) {
	t.Helper()

	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{},` +
		`"config":{"Entrypoint":["/init"],"Cmd":["serve"]}}`)
	layer := []byte("verified layer content")
	configDescriptor := testAPIDescriptor("application/vnd.oci.image.config.v1+json", config)
	layerDescriptor := testAPIDescriptor("application/vnd.oci.image.layer.v1.tar+gzip", layer)
	manifest := mustTestJSON(t, map[string]any{
		"schemaVersion":    2,
		testMediaTypeField: "application/vnd.oci.image.manifest.v1+json",
		"config":           descriptorDocument(configDescriptor, nil),
		"layers":           []any{descriptorDocument(layerDescriptor, nil)},
	})
	manifestDescriptor := testAPIDescriptor("application/vnd.oci.image.manifest.v1+json", manifest)
	platform := domain.Platform{OS: "linux", Architecture: "amd64"}
	index := mustTestJSON(t, map[string]any{
		"schemaVersion":    2,
		testMediaTypeField: "application/vnd.oci.image.index.v1+json",
		"manifests": []any{descriptorDocument(manifestDescriptor, map[string]string{
			"os": platform.OS, "architecture": platform.Architecture,
		})},
	})
	indexDescriptor := testAPIDescriptor("application/vnd.oci.image.index.v1+json", index)
	source, err := imageref.Normalize(testContainerdImage)
	if err != nil {
		t.Fatal(err)
	}

	return &testContainerdState{
		mutex: sync.Mutex{},
		image: &imagesapi.Image{Name: testContainerdImage, Target: indexDescriptor},
		blobs: map[string][]byte{
			indexDescriptor.GetDigest(): index, manifestDescriptor.GetDigest(): manifest,
			configDescriptor.GetDigest(): config, layerDescriptor.GetDigest(): layer,
		},
		reads: nil, imageStatus: codes.OK, missing: "", corrupt: "", offset: "", truncate: "",
		infoConflict: "", scopeCalls: 0, driftAfter: 0, uuid: "test-containerd",
	}, source, platform, layerDescriptor.GetDigest()
}

func testAPIDescriptor(mediaType string, raw []byte) *containertypes.Descriptor {
	return &containertypes.Descriptor{
		MediaType: mediaType, Digest: domain.Hash(raw).String(), Size: int64(len(raw)), Annotations: nil,
	}
}

func descriptorDocument(descriptorValue *containertypes.Descriptor, platform map[string]string) map[string]any {
	value := map[string]any{
		testMediaTypeField: descriptorValue.GetMediaType(),
		"digest":           descriptorValue.GetDigest(),
		"size":             descriptorValue.GetSize(),
	}
	if platform != nil {
		value["platform"] = platform
	}

	return value
}

func mustTestJSON(t *testing.T, value any) []byte {
	t.Helper()

	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func connectTestContainerd(t *testing.T, state *testContainerdState) *Client {
	t.Helper()

	path, stop := startTestContainerd(t, state)
	t.Cleanup(stop)
	client, err := Connect(context.Background(), "unix://"+path, testContainerdNamespace)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	return client
}

func startTestContainerd(t *testing.T, state *testContainerdState) (string, func()) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "containerd.sock")
	listener := listenTestUnix(t, path)
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	server := grpc.NewServer()
	imagesapi.RegisterImagesServer(server, &testImagesServer{state: state})
	contentapi.RegisterContentServer(server, &testContentServer{state: state})
	introspectionapi.RegisterIntrospectionServer(server, &testIntrospectionServer{state: state})
	versionapi.RegisterVersionServer(server, &testVersionServer{})
	done := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(done)
	}()
	var once sync.Once

	return path, func() {
		once.Do(func() {
			server.Stop()
			_ = listener.Close()
			<-done
		})
	}
}

func TestContainerdFixtureFailureMessageIsPrivate(t *testing.T) {
	t.Parallel()

	message := fmt.Sprint(registryTransportError(status.Error(codes.Unknown, "sensitive")))
	if message != registry.ErrProtocol.Error() {
		t.Fatalf("registryTransportError() = %q", message)
	}
}
