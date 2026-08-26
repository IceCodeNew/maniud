// Package podman implements the native, versioned Libpod REST adapter.
package podman

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	minimumLibpodAPIVersion = "4.3.1"
	maximumLibpodAPIVersion = "6.1.0"
	podmanJSONType          = "application/json"
	podmanDummyHost         = "podman.invalid"
	maximumControlBytes     = int64(16 << 20)
	podmanRequestTimeout    = 30 * time.Second
	podmanContentType       = "Content-Type"
)

var (
	// ErrInvalidEndpoint reports a Podman endpoint outside the local Unix-socket contract.
	ErrInvalidEndpoint = errors.New("podman endpoint is invalid")
	// ErrUnavailable reports a transport failure without exposing endpoint details.
	ErrUnavailable = errors.New("podman service is unavailable")
	// ErrProtocol reports an incompatible or malformed Libpod response.
	ErrProtocol = errors.New("podman service protocol is invalid")
	// ErrUnsupportedWorkload reports desired state outside the Podman adapter contract.
	ErrUnsupportedWorkload = errors.New("podman workload is unsupported")
)

type socketIdentity struct {
	device uint64
	inode  uint64
	owner  uint32
	mode   uint32
}

type peerIdentity struct {
	process    int32
	owner      uint32
	group      uint32
	generation uint64
}

// Version is fixed-schema Libpod and server product evidence.
type Version struct {
	Protocol     string
	Minimum      string
	Maximum      string
	Product      string
	OS           string
	Architecture string
}

// Client is one local Podman service fixed to a Libpod schema and daemon scope.
type Client struct {
	httpClient *http.Client
	baseURL    url.URL
	socketPath string
	socket     socketIdentity
	peer       peerIdentity
	version    Version
	protocol   semanticVersion
	scope      domain.Digest

	peerLock sync.Mutex
}

// Connect authenticates a local Unix socket, negotiates Libpod 4.3.1 through
// 6.1.0, and pins the daemon storage scope used by every later request.
func Connect(ctx context.Context, socketPath string) (*Client, Version, error) {
	var empty Version

	identity, err := inspectSocket(socketPath)
	if err != nil {
		return nil, empty, err
	}

	client := &Client{
		httpClient: nil,
		baseURL: url.URL{ //nolint:exhaustruct // A request base has no path, query, user, or fragment.
			Scheme: "http",
			Host:   podmanDummyHost,
		},
		socketPath: socketPath,
		socket:     identity,
		peer:       peerIdentity{},
		version:    empty,
		protocol:   semanticVersion{},
		scope:      domain.Digest{},
		peerLock:   sync.Mutex{},
	}
	client.httpClient = podmanHTTPClient(client)

	version, scope, err := client.negotiate(ctx)
	if err != nil {
		client.CloseIdleConnections()

		return nil, empty, err
	}
	client.version = version
	client.scope = scope

	return client, version, nil
}

// Version returns immutable negotiated protocol and server evidence.
func (client *Client) Version() Version {
	if client == nil {
		return Version{}
	}

	return client.version
}

// CloseIdleConnections closes pooled transport connections.
func (client *Client) CloseIdleConnections() {
	if client != nil && client.httpClient != nil {
		client.httpClient.CloseIdleConnections()
	}
}

func (client *Client) request(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	headers http.Header,
	pull bool,
) (*http.Response, error) {
	return client.requestWithBody(ctx, method, path, query, headers, nil, pull)
}

func (client *Client) requestWithBody(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	headers http.Header,
	body []byte,
	pull bool,
) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}

	return client.requestWithReader(ctx, method, path, query, headers, reader, pull)
}

func (client *Client) requestWithReader(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	headers http.Header,
	body io.Reader,
	pull bool,
) (*http.Response, error) {
	if !client.ready(path) {
		return nil, ErrProtocol
	}
	before, err := inspectSocket(client.socketPath)
	if err != nil || before != client.socket {
		return nil, ErrInvalidEndpoint
	}

	request, err := client.newRequestWithReader(ctx, method, path, query, headers, body)
	if err != nil {
		return nil, err
	}
	response, err := client.doRequest(ctx, request, pull)
	if err != nil {
		return nil, err
	}
	after, inspectErr := inspectSocket(client.socketPath)
	if inspectErr != nil || after != before {
		_ = response.Body.Close()

		return nil, ErrInvalidEndpoint
	}

	return response, nil
}

func (client *Client) ready(path string) bool {
	return client != nil && client.httpClient != nil && strings.HasPrefix(path, "/") &&
		client.socketPath != "" && client.socket != (socketIdentity{})
}

func (client *Client) apiPath(path string) string {
	if !client.negotiated() {
		return ""
	}

	return "/v" + client.protocol.String() + "/libpod" + path
}

func (client *Client) negotiated() bool {
	return client != nil && client.version.Protocol == client.protocol.String() &&
		validNegotiatedLibpodVersion(client.version)
}

func (client *Client) newRequest(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	headers http.Header,
) (*http.Request, error) {
	return client.newRequestWithReader(ctx, method, path, query, headers, nil)
}

func (client *Client) newRequestWithReader(
	ctx context.Context,
	method string,
	path string,
	query url.Values,
	headers http.Header,
	body io.Reader,
) (*http.Request, error) {
	endpoint := client.baseURL
	endpoint.Path = path
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, ErrProtocol
	}
	request.Header.Set("Accept", podmanJSONType)
	for key, values := range headers {
		request.Header.Del(key)
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}

	return request, nil
}

func (client *Client) doRequest(
	ctx context.Context,
	request *http.Request,
	pull bool,
) (*http.Response, error) {
	httpClient := client.httpClient
	if pull {
		clone := *client.httpClient
		clone.Timeout = 0
		httpClient = &clone
	}
	response, err := httpClient.Do(request)
	if err == nil {
		return response, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, fmt.Errorf("podman service request: %w", ctxErr)
	}

	return nil, ErrUnavailable
}

func closePodmanResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}
