// Package containerd implements image analysis and workload transactions over
// containerd's native gRPC API.
package containerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	contentapi "github.com/containerd/containerd/api/services/content/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	versionapi "github.com/containerd/containerd/api/services/version/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

const (
	containerdNamespaceHeader = "containerd-namespace"
	maximumNamespaceBytes     = 76
	maximumScopeTextBytes     = 4096
	maximumMetadataBytes      = int64(8 << 20)
	maximumGRPCMessageBytes   = 8<<20 + 64<<10
	maximumContentChunks      = 16 << 10
	containerdRequestTimeout  = 60 * time.Second
	containerdDialTimeout     = 10 * time.Second
)

var (
	// ErrInvalidEndpoint reports an endpoint or namespace outside the explicit
	// local Unix-socket contract.
	ErrInvalidEndpoint = errors.New("containerd endpoint is invalid")
	// ErrUnavailable reports an unavailable or changed containerd service.
	ErrUnavailable = errors.New("containerd service is unavailable")
	// ErrProtocol reports malformed or unverifiable containerd evidence.
	ErrProtocol     = errors.New("containerd service protocol is invalid")
	errCancelled    = errors.New("containerd operation was cancelled")
	errNotFound     = errors.New("containerd image content was not found")
	errRateLimited  = errors.New("containerd operation was rate limited")
	errUnauthorized = errors.New("containerd operation was not authorized")

	namespacePattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*$`)
)

type imagesClient interface {
	Get(
		ctx context.Context,
		request *imagesapi.GetImageRequest,
		options ...grpc.CallOption,
	) (*imagesapi.GetImageResponse, error)
}

type contentClient interface {
	Info(
		ctx context.Context,
		request *contentapi.InfoRequest,
		options ...grpc.CallOption,
	) (*contentapi.InfoResponse, error)
	Read(
		ctx context.Context,
		request *contentapi.ReadContentRequest,
		options ...grpc.CallOption,
	) (contentapi.Content_ReadClient, error)
}

type introspectionClient interface {
	Server(
		ctx context.Context,
		request *emptypb.Empty,
		options ...grpc.CallOption,
	) (*introspectionapi.ServerResponse, error)
}

type versionClient interface {
	Version(
		ctx context.Context,
		request *emptypb.Empty,
		options ...grpc.CallOption,
	) (*versionapi.VersionResponse, error)
}

type grpcClientFactory func(string, ...grpc.DialOption) (*grpc.ClientConn, error)

type socketIdentity struct {
	device     uint64
	inode      uint64
	owner      uint32
	group      uint32
	mode       uint32
	changeTime int64
}

type peerIdentity struct {
	process    uint64
	owner      uint32
	group      uint32
	generation uint64
}

type runtimeScope struct {
	version  string
	revision string
	uuid     string
	process  uint64
	pidns    uint64
}

// Client is one namespace-pinned connection to a local containerd service.
type Client struct {
	connection    io.Closer
	images        imagesClient
	content       contentClient
	introspection introspectionClient
	version       versionClient
	workloads     workloadBackend
	socketPath    string
	namespace     string
	socket        socketIdentity
	scope         runtimeScope

	peerLock sync.Mutex
	peer     peerIdentity
}

// Connect authenticates one local Unix socket and pins the selected namespace
// and containerd server identity. Address may be an absolute path or unix://
// followed by one.
func Connect(ctx context.Context, address, namespace string) (*Client, error) {
	return ConnectWithWorkloadOptions(ctx, address, namespace, DefaultWorkloadOptions())
}

// ConnectWithWorkloadOptions authenticates one local containerd endpoint and
// configures the host-side resources used for workload execution.
func ConnectWithWorkloadOptions(
	ctx context.Context,
	address string,
	namespace string,
	options WorkloadOptions,
) (*Client, error) {
	return connect(ctx, address, namespace, options, grpc.NewClient)
}

func connect(
	ctx context.Context,
	address string,
	namespace string,
	options WorkloadOptions,
	newClient grpcClientFactory,
) (*Client, error) {
	path, valid := endpointPath(address)
	if !valid || !validNamespace(namespace) || !options.valid() || newClient == nil {
		return nil, ErrInvalidEndpoint
	}
	socket, err := inspectSocket(path)
	if err != nil {
		return nil, err
	}

	client := &Client{
		connection: nil,
		images:     nil, content: nil, introspection: nil, version: nil,
		workloads:  nil,
		socketPath: path, namespace: namespace, socket: socket, scope: runtimeScope{},
		peerLock: sync.Mutex{}, peer: peerIdentity{},
	}
	grpcConnection, err := newClient(
		"passthrough:///containerd",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(client.dial),
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(maximumGRPCMessageBytes),
			grpc.MaxCallSendMsgSize(maximumGRPCMessageBytes),
		),
	)
	if err != nil || grpcConnection == nil {
		return nil, ErrUnavailable
	}
	client.connection = grpcConnection
	client.images = imagesapi.NewImagesClient(grpcConnection)
	client.content = contentapi.NewContentClient(grpcConnection)
	client.introspection = introspectionapi.NewIntrospectionClient(grpcConnection)
	client.version = versionapi.NewVersionClient(grpcConnection)
	client.workloads = newNativeWorkloadBackend(grpcConnection, namespace, options)

	scope, err := client.currentScope(ctx)
	if err != nil {
		_ = grpcConnection.Close()

		return nil, err
	}
	client.scope = scope

	return client, nil
}

// Close releases the gRPC connection.
func (client *Client) Close() error {
	if client == nil || client.connection == nil {
		return nil
	}

	if err := client.connection.Close(); err != nil {
		return fmt.Errorf("close containerd connection: %w", err)
	}

	return nil
}

// CloseIdleConnections releases the gRPC connection through the common
// runtime cleanup contract.
func (client *Client) CloseIdleConnections() {
	_ = client.Close()
}

func endpointPath(address string) (string, bool) {
	path := strings.TrimPrefix(address, "unix://")

	return path, address != "" && strings.TrimSpace(address) == address &&
		(address == path || strings.HasPrefix(address, "unix://")) && filepath.IsAbs(path) &&
		filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func validNamespace(namespace string) bool {
	return len(namespace) <= maximumNamespaceBytes && utf8.ValidString(namespace) &&
		namespacePattern.MatchString(namespace)
}

func inspectSocket(path string) (socketIdentity, error) {
	metadataValue, err := os.Lstat(path)
	if err != nil || metadataValue.Mode()&os.ModeSocket == 0 || metadataValue.Mode()&os.ModeSymlink != 0 {
		return socketIdentity{}, ErrInvalidEndpoint
	}

	return authenticateSocketMetadata(metadataValue)
}

func authenticateSocketMetadata(metadataValue os.FileInfo) (socketIdentity, error) {
	identity, valid := socketMetadata(metadataValue)
	if !valid || !allowedOwner(identity.owner) {
		return socketIdentity{}, ErrInvalidEndpoint
	}

	return identity, nil
}

func (client *Client) dial(ctx context.Context, _ string) (net.Conn, error) {
	current, err := inspectSocket(client.socketPath)
	if err != nil || current != client.socket {
		return nil, ErrInvalidEndpoint
	}
	connection, err := (&net.Dialer{ //nolint:exhaustruct // Unix sockets do not use TCP keepalive fields.
		Timeout: containerdDialTimeout,
	}).DialContext(ctx, "unix", client.socketPath)
	if err != nil {
		if ctx.Err() != nil {
			return nil, errCancelled
		}

		return nil, ErrUnavailable
	}
	peer, err := connectedPeer(connection)
	current, inspectErr := inspectSocket(client.socketPath)
	if err != nil || inspectErr != nil || current != client.socket ||
		peer.owner != client.socket.owner || !client.pinPeer(peer) {
		_ = connection.Close()

		return nil, ErrInvalidEndpoint
	}

	return connection, nil
}

func (client *Client) pinPeer(peer peerIdentity) bool {
	client.peerLock.Lock()
	defer client.peerLock.Unlock()

	if client.peer == (peerIdentity{}) {
		client.peer = peer
	}

	return client.peer == peer
}

func allowedOwner(owner uint32) bool {
	return owner == 0 || owner == uint32(os.Geteuid()) //nolint:gosec // Native Unix UIDs are non-negative.
}

func (client *Client) operationContext(ctx context.Context) (context.Context, context.CancelFunc) {
	bounded, cancel := context.WithTimeout(ctx, containerdRequestTimeout)

	return metadata.NewOutgoingContext(
		bounded,
		metadata.Pairs(containerdNamespaceHeader, client.namespace),
	), cancel
}

func (client *Client) currentScope(ctx context.Context) (runtimeScope, error) {
	if client == nil || client.version == nil || client.introspection == nil {
		return runtimeScope{}, ErrUnavailable
	}
	requestContext, cancel := client.operationContext(ctx)
	defer cancel()

	return client.readScope(requestContext)
}

func (client *Client) readScope(ctx context.Context) (runtimeScope, error) {
	if client == nil || client.version == nil || client.introspection == nil {
		return runtimeScope{}, ErrUnavailable
	}

	versionValue, err := client.version.Version(ctx, &emptypb.Empty{})
	if err != nil {
		return runtimeScope{}, classifyRPCError(err)
	}
	server, err := client.introspection.Server(ctx, &emptypb.Empty{})
	if err != nil {
		return runtimeScope{}, classifyRPCError(err)
	}

	return scopeFromResponses(versionValue, server)
}

func scopeFromResponses(
	versionValue *versionapi.VersionResponse,
	server *introspectionapi.ServerResponse,
) (runtimeScope, error) {
	if versionValue == nil || server == nil || !validScopeText(versionValue.GetVersion()) ||
		!validOptionalScopeText(versionValue.GetRevision()) || !validScopeText(server.GetUUID()) ||
		server.GetPid() == 0 || server.GetPidns() == 0 {
		return runtimeScope{}, ErrProtocol
	}

	return runtimeScope{
		version: versionValue.GetVersion(), revision: versionValue.GetRevision(), uuid: server.GetUUID(),
		process: server.GetPid(), pidns: server.GetPidns(),
	}, nil
}

func validScopeText(value string) bool {
	return value != "" && validOptionalScopeText(value)
}

func validOptionalScopeText(value string) bool {
	return len(value) <= maximumScopeTextBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func (client *Client) checked(ctx context.Context, operation func(context.Context) error) error {
	if operation == nil {
		return ErrProtocol
	}
	requestContext, cancel := client.operationContext(ctx)
	defer cancel()

	if err := client.requireScope(requestContext); err != nil {
		return err
	}
	if err := operation(requestContext); err != nil {
		return err
	}

	return client.requireScope(requestContext)
}

func (client *Client) requireScope(ctx context.Context) error {
	currentSocket, err := inspectSocket(client.socketPath)
	if err != nil || currentSocket != client.socket {
		return ErrUnavailable
	}
	currentScope, err := client.readScope(ctx)
	if err != nil {
		return err
	}
	if currentScope != client.scope {
		return ErrUnavailable
	}

	return nil
}
