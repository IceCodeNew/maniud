package containerd

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	versionapi "github.com/containerd/containerd/api/services/version/v1"
	containertypes "github.com/containerd/containerd/api/types"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
)

var errContainerdTest = errors.New("containerd test failure")

type fakeVersionClient struct {
	response *versionapi.VersionResponse
	err      error
}

func (client fakeVersionClient) Version(
	context.Context,
	*emptypb.Empty,
	...grpc.CallOption,
) (*versionapi.VersionResponse, error) {
	return client.response, client.err
}

type fakeIntrospectionClient struct {
	responses []*introspectionapi.ServerResponse
	err       error
	index     int
}

func (client *fakeIntrospectionClient) Server(
	context.Context,
	*emptypb.Empty,
	...grpc.CallOption,
) (*introspectionapi.ServerResponse, error) {
	if client.err != nil {
		return nil, client.err
	}
	index := client.index
	if index >= len(client.responses) {
		index = len(client.responses) - 1
	}
	client.index++

	return client.responses[index], nil
}

type fakeImagesClient struct {
	response *imagesapi.GetImageResponse
	err      error
}

func (client fakeImagesClient) Get(
	context.Context,
	*imagesapi.GetImageRequest,
	...grpc.CallOption,
) (*imagesapi.GetImageResponse, error) {
	return client.response, client.err
}

type fakeContentClient struct {
	info      *contentapi.InfoResponse
	infoErr   error
	stream    contentapi.Content_ReadClient
	streamErr error
}

func (client fakeContentClient) Info(
	context.Context,
	*contentapi.InfoRequest,
	...grpc.CallOption,
) (*contentapi.InfoResponse, error) {
	return client.info, client.infoErr
}

//nolint:ireturn // The generated gRPC client interface requires this stream type.
func (client fakeContentClient) Read(
	context.Context,
	*contentapi.ReadContentRequest,
	...grpc.CallOption,
) (contentapi.Content_ReadClient, error) {
	return client.stream, client.streamErr
}

type fakeContentStream struct {
	responses []*contentapi.ReadContentResponse
	err       error
	index     int
}

func (stream *fakeContentStream) Recv() (*contentapi.ReadContentResponse, error) {
	if stream.index < len(stream.responses) {
		response := stream.responses[stream.index]
		stream.index++

		return response, nil
	}
	if stream.err != nil {
		return nil, stream.err
	}

	return nil, io.EOF
}

func (*fakeContentStream) Header() (metadata.MD, error) { return metadata.MD{}, nil }
func (*fakeContentStream) Trailer() metadata.MD         { return nil }
func (*fakeContentStream) CloseSend() error             { return nil }
func (*fakeContentStream) Context() context.Context     { return context.Background() }
func (*fakeContentStream) SendMsg(any) error            { return nil }
func (*fakeContentStream) RecvMsg(any) error            { return nil }

type fakeFileInfo struct{}

func (fakeFileInfo) Name() string       { return "fake" }
func (fakeFileInfo) Size() int64        { return 0 }
func (fakeFileInfo) Mode() os.FileMode  { return os.ModeSocket }
func (fakeFileInfo) ModTime() time.Time { return time.Time{} }
func (fakeFileInfo) IsDir() bool        { return false }
func (fakeFileInfo) Sys() any           { return nil }

type fakeCloser struct {
	err error
}

func (closer fakeCloser) Close() error { return closer.err }

func TestContainerdEndpointAndNamespaceValidation(t *testing.T) {
	t.Parallel()

	invalidAddresses := []string{
		"", testRelativePath, " tcp:///tmp/x", "tcp:///tmp/x", "unix://relative", "/tmp/../tmp/x", "/tmp/x\x00",
	}
	for _, address := range invalidAddresses {
		if _, valid := endpointPath(address); valid {
			t.Fatalf("endpointPath(%q) accepted", address)
		}
	}
	for _, address := range []string{"/tmp/containerd.sock", "unix:///tmp/containerd.sock"} {
		if path, valid := endpointPath(address); !valid || path != "/tmp/containerd.sock" {
			t.Fatalf("endpointPath(%q) = %q, %v", address, path, valid)
		}
	}
	for _, namespace := range []string{"", "bad namespace", "-bad", "bad-", strings.Repeat("a", maximumNamespaceBytes+1)} {
		if validNamespace(namespace) {
			t.Fatalf("validNamespace(%q) accepted", namespace)
		}
	}
	if !validNamespace("moby.test_1") {
		t.Fatal("validNamespace(valid) rejected")
	}
}

func TestContainerdUnixInputValidation(t *testing.T) {
	t.Parallel()

	if client, err := Connect(context.Background(), "relative", "invalid namespace"); client != nil ||
		!errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("Connect(invalid) = %#v, %v", client, err)
	}
	if client, err := Connect(context.Background(), "/missing/containerd.sock", "valid"); client != nil ||
		!errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("Connect(missing) = %#v, %v", client, err)
	}
	regular := t.TempDir() + "/regular"
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectSocket(regular); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("inspectSocket(regular) = %v", err)
	}
	if _, valid := socketMetadata(fakeFileInfo{}); valid {
		t.Fatal("socketMetadata(fake) accepted")
	}
	if _, err := connectedPeer(nil); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(nil) = %v", err)
	}
	if _, err := connectedPeer(&net.UnixConn{}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(empty) = %v", err)
	}
}

func TestContainerdNilBoundaries(t *testing.T) {
	t.Parallel()

	var nilClient *Client
	nilClient.CloseIdleConnections()
	if err := nilClient.Close(); err != nil {
		t.Fatalf("Close(nil) = %v", err)
	}
	if err := (&Client{}).Close(); err != nil {
		t.Fatalf("Close(empty) = %v", err)
	}
	if _, err := nilClient.currentScope(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("currentScope(nil) = %v", err)
	}
	if _, err := nilClient.readScope(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("readScope(nil) = %v", err)
	}
	_, err := nilClient.Resolve(context.Background(), imageref.Source{}, domain.Platform{})
	if !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("Resolve(nil) = %v", err)
	}
	if descriptorValue := apiDescriptor(nil); !reflect.DeepEqual(descriptorValue, ocispec.Descriptor{}) {
		t.Fatalf("apiDescriptor(nil) = %#v", descriptorValue)
	}
}

func TestContainerdCloseAndLocalResult(t *testing.T) {
	t.Parallel()

	client := &Client{connection: fakeCloser{err: errContainerdTest}}
	if err := client.Close(); !errors.Is(err, errContainerdTest) {
		t.Fatalf("Close(failure) = %v", err)
	}
	image := domain.ImageIdentity{Reference: testContainerdImage}
	if got, err := localImageResult(image, nil, nil); err != nil || got.Reference != image.Reference {
		t.Fatalf("localImageResult(success) = %#v, %v", got, err)
	}
	if _, err := localImageResult(image, errContainerdTest, nil); !errors.Is(err, errContainerdTest) {
		t.Fatalf("localImageResult(resolve failure) = %v", err)
	}
	if _, err := localImageResult(image, nil, errContainerdTest); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("localImageResult(close failure) = %v", err)
	}
}

func listenTestUnix(t *testing.T, path string) net.Listener {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}

	return listener
}

func testContainerdSocketPath(t *testing.T, name string) string {
	t.Helper()

	directory, err := os.MkdirTemp("/tmp", "maniud-containerd-") //nolint:usetesting // Darwin socket paths are short.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })

	return filepath.Join(directory, name)
}

func TestConnectContainsGRPCClientConstructionFailures(t *testing.T) {
	t.Parallel()

	path := testContainerdSocketPath(t, "containerd.sock")
	listener := listenTestUnix(t, path)
	t.Cleanup(func() { _ = listener.Close() })

	for _, factory := range []grpcClientFactory{
		nil,
		func(string, ...grpc.DialOption) (*grpc.ClientConn, error) { return nil, errContainerdTest },
	} {
		client, err := connect(
			context.Background(), path, testContainerdScopeText, DefaultWorkloadOptions(), factory,
		)
		if client != nil || err == nil {
			t.Fatalf("connect(failing factory) = %#v, %v", client, err)
		}
	}
}

func TestContainerdScopeFailures(t *testing.T) {
	t.Parallel()

	validVersion := &versionapi.VersionResponse{Version: testContainerdVersion, Revision: testContainerdScopeText}
	validServer := &introspectionapi.ServerResponse{UUID: testContainerdServerUUID, Pid: 1, Pidns: 2}
	for _, test := range []struct {
		name          string
		version       fakeVersionClient
		introspection *fakeIntrospectionClient
		want          error
	}{
		{
			name: "version error", version: fakeVersionClient{err: status.Error(codes.Unavailable, "down")},
			introspection: &fakeIntrospectionClient{responses: []*introspectionapi.ServerResponse{validServer}},
			want:          ErrUnavailable,
		},
		{
			name: "introspection error", version: fakeVersionClient{response: validVersion},
			introspection: &fakeIntrospectionClient{err: status.Error(codes.Unavailable, "down")},
			want:          ErrUnavailable,
		},
		{
			name: "nil version", version: fakeVersionClient{},
			introspection: &fakeIntrospectionClient{responses: []*introspectionapi.ServerResponse{validServer}},
			want:          ErrProtocol,
		},
		{
			name: "nil server", version: fakeVersionClient{response: validVersion},
			introspection: &fakeIntrospectionClient{responses: []*introspectionapi.ServerResponse{nil}},
			want:          ErrProtocol,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &Client{
				version: test.version, introspection: test.introspection, namespace: testContainerdScopeText,
			}
			if _, err := client.readScope(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("readScope() = %v", err)
			}
		})
	}
}

func TestContainerdCheckedOperationFailures(t *testing.T) {
	t.Parallel()

	validVersion := &versionapi.VersionResponse{Version: testContainerdVersion, Revision: testContainerdScopeText}
	validServer := &introspectionapi.ServerResponse{UUID: testContainerdServerUUID, Pid: 1, Pidns: 2}
	client := scopeTestClient(t, validVersion, validServer, validServer)
	if err := client.checked(context.Background(), nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("checked(nil) = %v", err)
	}
	err := client.checked(context.Background(), func(context.Context) error { return errContainerdTest })
	if !errors.Is(err, errContainerdTest) {
		t.Fatalf("checked(operation error) = %v", err)
	}
	client = scopeTestClient(t, validVersion, validServer)
	client.version = fakeVersionClient{}
	err = client.checked(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("checked(pre-operation failure) = %v", err)
	}
	drifted := &introspectionapi.ServerResponse{UUID: testChangedValue, Pid: 1, Pidns: 2}
	client = scopeTestClient(t, validVersion, validServer, drifted)
	err = client.checked(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("checked(drift) = %v", err)
	}
	client = scopeTestClient(t, validVersion, validServer)
	client.scope.uuid = "changed"
	err = client.checked(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("checked(pre-operation drift) = %v", err)
	}
	client = scopeTestClient(t, validVersion, validServer, nil)
	err = client.checked(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("checked(post-operation failure) = %v", err)
	}
	client = scopeTestClient(t, validVersion, validServer)
	client.socket.inode++
	err = client.checked(context.Background(), func(context.Context) error { return nil })
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("checked(socket drift) = %v", err)
	}
}

func scopeTestClient(
	t *testing.T,
	versionValue *versionapi.VersionResponse,
	initialServer *introspectionapi.ServerResponse,
	remainingServers ...*introspectionapi.ServerResponse,
) *Client {
	t.Helper()

	path := testContainerdSocketPath(t, "containerd-scope.sock")
	listener := listenTestUnix(t, path)
	t.Cleanup(func() { _ = listener.Close() })
	identity, err := inspectSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	servers := append([]*introspectionapi.ServerResponse{initialServer}, remainingServers...)

	return &Client{
		version:       fakeVersionClient{response: versionValue},
		introspection: &fakeIntrospectionClient{responses: servers},
		socketPath:    path, namespace: testContainerdScopeText, socket: identity,
		scope: runtimeScope{
			version: versionValue.GetVersion(), revision: versionValue.GetRevision(), uuid: initialServer.GetUUID(),
			process: initialServer.GetPid(), pidns: initialServer.GetPidns(),
		},
	}
}

func TestContainerdImageProtocolBoundaries(t *testing.T) {
	t.Parallel()

	source, err := imageref.Normalize(testContainerdImage)
	if err != nil {
		t.Fatal(err)
	}
	validVersion := &versionapi.VersionResponse{Version: testContainerdVersion}
	validServer := &introspectionapi.ServerResponse{UUID: testContainerdServerUUID, Pid: 1, Pidns: 2}
	client := scopeTestClient(t, validVersion, validServer, validServer)
	client.images = fakeImagesClient{response: &imagesapi.GetImageResponse{}}
	client.content = fakeContentClient{}
	repository := &imageRepository{client: client, source: source}
	if _, _, err := repository.FetchReference(context.Background(), "latest"); !errors.Is(err, registry.ErrProtocol) {
		t.Fatalf("FetchReference(malformed) = %v", err)
	}

	digestValue := domain.Hash([]byte("content"))
	for _, descriptorValue := range []ocispec.Descriptor{
		{},
		{Digest: digest.Digest(digestValue.String()), Size: -1},
		{Digest: digest.Digest(digestValue.String()), Size: maximumMetadataBytes + 1},
	} {
		if _, err := repository.Fetch(context.Background(), descriptorValue); !errors.Is(err, registry.ErrProtocol) {
			t.Fatalf("Fetch(%#v) = %v", descriptorValue, err)
		}
	}

	descriptorValue := ocispec.Descriptor{Digest: digest.Digest(digestValue.String()), Size: 7}
	client = scopeTestClient(t, validVersion, validServer, validServer, validServer)
	client.images = fakeImagesClient{}
	client.content = fakeContentClient{
		info:      &contentapi.InfoResponse{Info: &contentapi.Info{Digest: digestValue.String(), Size: 7}},
		streamErr: status.Error(codes.Unavailable, "down"),
	}
	repository = &imageRepository{client: client, source: source}
	if _, err := repository.Fetch(context.Background(), descriptorValue); !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("Fetch(stream error) = %v", err)
	}
}

func TestContentStreamBoundaries(t *testing.T) {
	t.Parallel()

	raw := []byte("content")
	digestValue := domain.Hash(raw)
	for _, test := range []struct {
		name    string
		stream  contentapi.Content_ReadClient
		size    int64
		digest  domain.Digest
		capture bool
		want    error
	}{
		{name: "nil stream", size: 7, digest: digestValue, want: ErrProtocol},
		{
			name: "receive failure", stream: &fakeContentStream{err: status.Error(codes.Unavailable, "down")},
			size: 7, digest: digestValue, want: ErrUnavailable,
		},
		{
			name: "oversized chunk", stream: &fakeContentStream{responses: []*contentapi.ReadContentResponse{{Data: raw}}},
			size: 6, digest: digestValue, want: ErrProtocol,
		},
		{
			name: "uncaptured success", stream: &fakeContentStream{responses: []*contentapi.ReadContentResponse{{Data: raw}}},
			size: 7, digest: digestValue,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := consumeContent(test.stream, test.size, test.digest, test.capture)
			if !errors.Is(err, test.want) || test.want == nil && len(got) != 0 {
				t.Fatalf("consumeContent() = %q, %v", got, err)
			}
		})
	}

	many := make([]*contentapi.ReadContentResponse, maximumContentChunks)
	_, err := consumeContent(
		&fakeContentStream{responses: many},
		1,
		domain.Hash([]byte{0}),
		false,
	)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("consumeContent(too many chunks) = %v", err)
	}
}

func TestContainerdErrorClassification(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		err  error
		want error
	}{
		{err: context.Canceled, want: errCancelled},
		{err: status.Error(codes.Canceled, "cancelled"), want: errCancelled},
		{err: status.Error(codes.NotFound, "missing"), want: errNotFound},
		{err: status.Error(codes.PermissionDenied, "denied"), want: errUnauthorized},
		{err: status.Error(codes.Unauthenticated, "unauthenticated"), want: errUnauthorized},
		{err: status.Error(codes.ResourceExhausted, "limited"), want: errRateLimited},
		{err: status.Error(codes.DeadlineExceeded, "timeout"), want: ErrUnavailable},
		{err: status.Error(codes.Unavailable, "unavailable"), want: ErrUnavailable},
		{err: nil, want: ErrProtocol},
		{err: status.Error(codes.InvalidArgument, "invalid"), want: ErrProtocol},
		{err: status.Error(codes.AlreadyExists, "exists"), want: ErrProtocol},
		{err: status.Error(codes.FailedPrecondition, "precondition"), want: ErrProtocol},
		{err: status.Error(codes.Aborted, "aborted"), want: ErrProtocol},
		{err: status.Error(codes.OutOfRange, "range"), want: ErrProtocol},
		{err: status.Error(codes.Unimplemented, "unimplemented"), want: ErrProtocol},
		{err: status.Error(codes.Internal, "internal"), want: ErrProtocol},
		{err: status.Error(codes.DataLoss, "data loss"), want: ErrProtocol},
		{err: errContainerdTest, want: ErrProtocol},
		{err: status.Error(codes.Code(99), "invalid"), want: ErrProtocol},
	} {
		if got := classifyRPCError(test.err); !errors.Is(got, test.want) {
			t.Fatalf("classifyRPCError(%v) = %v", test.err, got)
		}
	}
	for _, test := range []struct {
		err  error
		want error
	}{
		{err: errCancelled, want: registry.ErrCancelled},
		{err: errNotFound, want: registry.ErrNotFound},
		{err: errUnauthorized, want: registry.ErrUnauthorized},
		{err: errRateLimited, want: registry.ErrRateLimited},
		{err: ErrUnavailable, want: registry.ErrUnavailable},
		{err: ErrProtocol, want: registry.ErrProtocol},
	} {
		if got := registryTransportError(test.err); !errors.Is(got, test.want) {
			t.Fatalf("registryTransportError(%v) = %v", test.err, got)
		}
	}
}

func TestConnectedPeerRejectsClosedConnection(t *testing.T) {
	t.Parallel()

	path := testContainerdSocketPath(t, "containerd.sock")
	listener := listenTestUnix(t, path)
	t.Cleanup(func() { _ = listener.Close() })
	closedConnection, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := closedConnection.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := connectedPeer(closedConnection); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(closed) = %v", err)
	}
}

func TestContainerdDialRejectsIdentityChanges(t *testing.T) {
	t.Parallel()

	path := testContainerdSocketPath(t, "containerd.sock")
	listener := listenTestUnix(t, path)
	identity, err := inspectSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	client := &Client{socketPath: path, socket: identity, peer: peerIdentity{process: 1}}
	if _, err := client.dial(context.Background(), ""); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("dial(peer change) = %v", err)
	}
	unixListener, valid := listener.(*net.UnixListener)
	if !valid {
		t.Fatalf("listener type = %T", listener)
	}
	unixListener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	client.peer = peerIdentity{}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.dial(cancelled, ""); !errors.Is(err, errCancelled) {
		t.Fatalf("dial(cancelled) = %v", err)
	}
	if _, err := client.dial(context.Background(), ""); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("dial(closed listener) = %v", err)
	}
	client.socket.inode++
	if _, err := client.dial(context.Background(), ""); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("dial(changed socket) = %v", err)
	}
}

func TestAPIContainerdDescriptorMapping(t *testing.T) {
	t.Parallel()

	value := &containertypes.Descriptor{
		MediaType: "application/test", Digest: domain.Hash([]byte("x")).String(), Size: 1,
		Annotations: map[string]string{"key": "value"},
	}
	got := apiDescriptor(value)
	if got.MediaType != value.GetMediaType() || got.Digest.String() != value.GetDigest() || got.Size != value.GetSize() ||
		got.Annotations["key"] != "value" {
		t.Fatalf("apiDescriptor() = %#v", got)
	}
}
