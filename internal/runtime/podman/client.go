// Package podman implements the native, versioned Libpod REST adapter.
package podman

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	libpodAPIVersion     = "6.1.0"
	libpodPrefix         = "/v" + libpodAPIVersion + "/libpod"
	podmanJSONType       = "application/json"
	podmanDummyHost      = "podman.invalid"
	maximumControlBytes  = int64(16 << 20)
	maximumPingBytes     = int64(1024)
	maximumVersionBytes  = int64(1 << 20)
	maximumTextBytes     = 4096
	podmanDialTimeout    = 10 * time.Second
	podmanRequestTimeout = 30 * time.Second
	podmanHeaderTimeout  = 15 * time.Second
	podmanIdleTimeout    = 30 * time.Second
	podmanKeepAlive      = 30 * time.Second
	maximumIdleConns     = 6
	maximumHeaderBytes   = 64 << 10
	semanticVersionParts = 3
	podmanAPIHeader      = "Libpod-Api-Version"
	podmanContentType    = "Content-Type"
	podmanOSLinux        = "linux"
	podmanArchAMD64      = "amd64"
	podmanArchARM64      = "arm64"
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
	scope      domain.Digest

	peerLock sync.Mutex
}

// Connect authenticates a local Unix socket, negotiates Libpod 6.1.0, and pins
// the daemon storage scope used by every later request.
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

func podmanHTTPClient(client *Client) *http.Client {
	transport := &http.Transport{
		//nolint:exhaustruct // Unsupported hooks stay nil so the adapter owns every network path.
		Proxy:                  nil,
		ForceAttemptHTTP2:      false,
		DisableCompression:     true,
		MaxIdleConns:           maximumIdleConns,
		IdleConnTimeout:        podmanIdleTimeout,
		ResponseHeaderTimeout:  podmanHeaderTimeout,
		MaxResponseHeaderBytes: maximumHeaderBytes,
		DialContext:            client.dialContext,
	}

	return &http.Client{
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Jar:     nil,
		Timeout: podmanRequestTimeout,
	}
}

func (client *Client) dialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	connection, err := (&net.Dialer{ //nolint:exhaustruct // Zero values preserve standard local-socket behavior.
		Timeout: podmanDialTimeout, KeepAlive: podmanKeepAlive,
	}).DialContext(ctx, "unix", client.socketPath)
	if err != nil {
		return nil, ErrUnavailable
	}

	return client.authenticateConnection(connection)
}

func (client *Client) authenticateConnection(connection net.Conn) (net.Conn, error) {
	peer, err := connectedPeer(connection)
	if err != nil || peer.owner != effectiveUserID() {
		_ = connection.Close()

		return nil, ErrInvalidEndpoint
	}
	if !client.pinPeer(peer) {
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

func inspectSocket(path string) (socketIdentity, error) {
	var empty socketIdentity

	if !validSocketPath(path) {
		return empty, ErrInvalidEndpoint
	}
	metadata, err := os.Lstat(path)
	if err != nil || !safeSocketMetadata(metadata) {
		return empty, ErrInvalidEndpoint
	}

	return authenticateSocketMetadata(metadata)
}

func authenticateSocketMetadata(metadata os.FileInfo) (socketIdentity, error) {
	var empty socketIdentity

	identity, valid := socketMetadata(metadata)
	if !valid || identity.owner != effectiveUserID() {
		return empty, ErrInvalidEndpoint
	}

	return identity, nil
}

func validSocketPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func safeSocketMetadata(metadata os.FileInfo) bool {
	return metadata != nil && metadata.Mode()&os.ModeSocket != 0 &&
		metadata.Mode()&os.ModeSymlink == 0 && metadata.Mode().Perm()&0o022 == 0
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

func (client *Client) negotiate(ctx context.Context) (Version, domain.Digest, error) {
	var empty Version
	var emptyDigest domain.Digest

	maximum, err := client.ping(ctx)
	if err != nil {
		return empty, emptyDigest, err
	}
	version, err := client.serverVersion(ctx, maximum)
	if err != nil {
		return empty, emptyDigest, err
	}
	scope, err := client.inspectScope(ctx, version)
	if err != nil {
		return empty, emptyDigest, err
	}
	version = client.version

	return version, scope, nil
}

func (client *Client) ping(ctx context.Context) (string, error) {
	response, err := client.request(ctx, http.MethodGet, "/_ping", nil, nil, false)
	if err != nil {
		return "", err
	}
	defer closePodmanResponse(response)

	value, readErr := io.ReadAll(io.LimitReader(response.Body, maximumPingBytes+1))
	maximum := response.Header.Get(podmanAPIHeader)
	if readErr != nil || response.StatusCode != http.StatusOK || len(value) > int(maximumPingBytes) ||
		string(bytes.TrimSpace(value)) != "OK" || !validSemanticVersion(maximum) {
		return "", ErrProtocol
	}

	return maximum, nil
}

type versionResponse struct {
	Version    string             `json:"Version"`    //nolint:tagliatelle // Libpod wire field.
	Components []versionComponent `json:"Components"` //nolint:tagliatelle // Libpod wire field.
}

type versionComponent struct {
	Name    string            `json:"Name"`    //nolint:tagliatelle // Libpod wire field.
	Version string            `json:"Version"` //nolint:tagliatelle // Libpod wire field.
	Details map[string]string `json:"Details"` //nolint:tagliatelle // Libpod wire field.
}

func (client *Client) serverVersion(ctx context.Context, pingMaximum string) (Version, error) {
	var empty Version

	response, err := client.request(ctx, http.MethodGet, "/version", nil, nil, false)
	if err != nil {
		return empty, err
	}
	defer closePodmanResponse(response)

	var payload versionResponse
	if response.StatusCode != http.StatusOK || !isPodmanJSON(response.Header.Get(podmanContentType)) ||
		!decodePodmanJSON(response.Body, maximumVersionBytes, &payload) ||
		!validPodmanText(payload.Version) {
		return empty, ErrProtocol
	}
	engine, valid := podmanEngine(payload.Components)
	if !valid || engine.Version != payload.Version {
		return empty, ErrProtocol
	}
	serverMaximum := engine.Details["APIVersion"]
	serverMinimum := engine.Details["MinAPIVersion"]
	if serverMaximum != pingMaximum || !compatibleLibpodRange(serverMinimum, serverMaximum) {
		return empty, ErrProtocol
	}

	return Version{
		Protocol: libpodAPIVersion, Minimum: serverMinimum, Maximum: serverMaximum,
		Product: payload.Version, OS: "", Architecture: "",
	}, nil
}

func podmanEngine(components []versionComponent) (versionComponent, bool) {
	var engine versionComponent
	found := false
	for _, component := range components {
		if component.Name != "Podman Engine" {
			continue
		}
		if found {
			return versionComponent{}, false
		}
		engine = component
		found = true
	}

	return engine, found
}

type infoResponse struct {
	Host struct {
		OS   string `json:"os"`
		Arch string `json:"arch"`
	} `json:"host"`
	Store struct {
		GraphRoot string `json:"graphRoot"` //nolint:tagliatelle // Libpod wire field.
	} `json:"store"`
}

func (client *Client) inspectScope(ctx context.Context, version Version) (domain.Digest, error) {
	response, err := client.request(ctx, http.MethodGet, libpodPrefix+"/info", nil, nil, false)
	if err != nil {
		return domain.Digest{}, err
	}
	defer closePodmanResponse(response)

	payload, platform, err := decodePodmanInfo(response)
	if err != nil {
		return domain.Digest{}, err
	}
	client.peerLock.Lock()
	peer := client.peer
	client.peerLock.Unlock()
	if peer == (peerIdentity{}) {
		return domain.Digest{}, ErrInvalidEndpoint
	}

	evidence := client.scopeEvidence(payload, platform, version, peer)

	version.OS = platform.OS
	version.Architecture = platform.Architecture
	client.version = version

	return domain.Hash(evidence), nil
}

func decodePodmanInfo(response *http.Response) (infoResponse, domain.Platform, error) {
	var payload infoResponse
	if response.StatusCode != http.StatusOK || !isPodmanJSON(response.Header.Get(podmanContentType)) ||
		!decodePodmanJSON(response.Body, maximumControlBytes, &payload) ||
		!validPodmanText(payload.Host.OS) || !validPodmanText(payload.Host.Arch) ||
		!validGraphRoot(payload.Store.GraphRoot) {
		return infoResponse{}, domain.Platform{}, ErrProtocol
	}
	platform, valid := podmanPlatform(payload.Host.OS, payload.Host.Arch)
	if !valid {
		return infoResponse{}, domain.Platform{}, ErrUnsupportedWorkload
	}

	return payload, platform, nil
}

func validGraphRoot(value string) bool {
	return value != "" && filepath.IsAbs(value) && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func (client *Client) scopeEvidence(
	payload infoResponse,
	platform domain.Platform,
	version Version,
	peer peerIdentity,
) []byte {
	evidence := []byte{1}
	evidence = appendPodmanString(evidence, domain.RuntimePodman.String())
	evidence = appendPodmanString(evidence, version.Product)
	evidence = appendPodmanString(evidence, version.Protocol)
	evidence = appendPodmanString(evidence, platform.OS)
	evidence = appendPodmanString(evidence, platform.Architecture)
	evidence = appendPodmanString(evidence, platform.Variant)
	evidence = appendPodmanString(evidence, payload.Store.GraphRoot)
	evidence = binary.LittleEndian.AppendUint64(evidence, client.socket.device)
	evidence = binary.LittleEndian.AppendUint64(evidence, client.socket.inode)
	evidence = binary.LittleEndian.AppendUint32(evidence, client.socket.owner)
	evidence = binary.LittleEndian.AppendUint32(evidence, client.socket.mode)
	process := uint32(peer.process) //nolint:gosec // connectedPeer accepts positive int32 PIDs only.
	evidence = binary.LittleEndian.AppendUint32(evidence, process)
	evidence = binary.LittleEndian.AppendUint32(evidence, peer.owner)
	evidence = binary.LittleEndian.AppendUint32(evidence, peer.group)
	evidence = binary.LittleEndian.AppendUint64(evidence, peer.generation)

	return evidence
}

func decodePodmanJSON(reader io.Reader, maximum int64, target any) bool {
	value, valid := jsonstrict.Read(reader, maximum)
	if !valid {
		return false
	}

	return json.Unmarshal(value, target) == nil
}

func isPodmanJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)

	return err == nil && mediaType == podmanJSONType
}

func compatibleLibpodRange(minimum, maximum string) bool {
	want, validWant := parseSemanticVersion(libpodAPIVersion)
	minimumVersion, validMinimum := parseSemanticVersion(minimum)
	maximumVersion, validMaximum := parseSemanticVersion(maximum)

	return validWant && validMinimum && validMaximum &&
		!want.less(minimumVersion) && !maximumVersion.less(want)
}

type semanticVersion struct {
	major uint64
	minor uint64
	patch uint64
}

func validSemanticVersion(value string) bool {
	_, valid := parseSemanticVersion(value)

	return valid
}

func parseSemanticVersion(value string) (semanticVersion, bool) {
	var empty semanticVersion
	parts := strings.Split(value, ".")
	if len(parts) != semanticVersionParts {
		return empty, false
	}
	values := [3]uint64{}
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return empty, false
		}
		parsed, err := strconv.ParseUint(part, 10, 31)
		if err != nil {
			return empty, false
		}
		values[index] = parsed
	}

	return semanticVersion{major: values[0], minor: values[1], patch: values[2]}, true
}

func (version semanticVersion) less(other semanticVersion) bool {
	if version.major != other.major {
		return version.major < other.major
	}
	if version.minor != other.minor {
		return version.minor < other.minor
	}

	return version.patch < other.patch
}

func validPodmanText(value string) bool {
	return value != "" && len(value) <= maximumTextBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}

func appendPodmanString(encoded []byte, value string) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(value)))

	return append(encoded, value...)
}

func podmanPlatform(osName, architecture string) (domain.Platform, bool) {
	if osName != podmanOSLinux {
		return domain.Platform{}, false
	}
	switch architecture {
	case podmanArchAMD64:
		return domain.Platform{OS: osName, Architecture: architecture, Variant: ""}, true
	case podmanArchARM64:
		return domain.Platform{OS: osName, Architecture: architecture, Variant: "v8"}, true
	default:
		return domain.Platform{}, false
	}
}

func effectiveUserID() uint32 {
	return uint32(os.Geteuid()) //nolint:gosec // Unix effective user IDs use the uint32 uid_t domain.
}

func closePodmanResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
}
