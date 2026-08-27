package podman

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

var errPodmanClientTest = errors.New("podman client test failure")

const (
	libpodAPIVersion           = maximumLibpodAPIVersion
	libpodPrefix               = "/v" + libpodAPIVersion + "/libpod"
	testLibpodServerMinimum    = "4.0.0"
	testLibpodMiddleVersion    = "5.4.2"
	testLibpodLaterVersion     = "5.8.3"
	testPodmanPingPath         = "/_ping"
	testPodmanFallbackPingPath = "/libpod/_ping"
	testPodmanVersionPath      = "/version"
	testPodmanMaximumInfoPath  = "/v" + maximumLibpodAPIVersion + "/libpod/info"
)

type podmanRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip podmanRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

type podmanNegotiationTest struct {
	name      string
	minimum   string
	maximum   string
	protocol  string
	pingRoute string
	wantPaths []string
}

type podmanNegotiationEvidence struct {
	Version       Version
	ClientVersion Version
	ScopePinned   bool
	PeerPinned    bool
	Protocol      string
	APIPath       string
	Requests      []string
}

func TestConnectNegotiatesAndPinsNativeLibpodScope(t *testing.T) {
	t.Parallel()

	tests := []podmanNegotiationTest{
		{
			name: "minimum", minimum: testLibpodServerMinimum, maximum: minimumLibpodAPIVersion,
			protocol: minimumLibpodAPIVersion, pingRoute: testPodmanPingPath,
			wantPaths: []string{testPodmanPingPath, testPodmanVersionPath, "/v4.3.1/libpod/info"},
		},
		{
			name: "middle", minimum: testLibpodServerMinimum,
			maximum: testLibpodMiddleVersion, protocol: testLibpodMiddleVersion, pingRoute: testPodmanPingPath,
			wantPaths: []string{testPodmanPingPath, testPodmanVersionPath, "/v5.4.2/libpod/info"},
		},
		{
			name: testLibpodLaterVersion, minimum: testLibpodServerMinimum,
			maximum: testLibpodLaterVersion, protocol: testLibpodLaterVersion, pingRoute: testPodmanPingPath,
			wantPaths: []string{
				testPodmanPingPath, testPodmanVersionPath, "/v" + testLibpodLaterVersion + "/libpod/info",
			},
		},
		{
			name: "maximum", minimum: testLibpodServerMinimum, maximum: maximumLibpodAPIVersion,
			protocol: maximumLibpodAPIVersion, pingRoute: testPodmanPingPath,
			wantPaths: []string{testPodmanPingPath, testPodmanVersionPath, testPodmanMaximumInfoPath},
		},
		{
			name: "future maximum", minimum: testLibpodServerMinimum, maximum: "7.0.0",
			protocol: maximumLibpodAPIVersion, pingRoute: testPodmanFallbackPingPath,
			wantPaths: []string{
				testPodmanPingPath, testPodmanFallbackPingPath, testPodmanVersionPath, testPodmanMaximumInfoPath,
			},
		},
		{
			name: "future prerelease maximum", minimum: testLibpodServerMinimum, maximum: "7.0.0-dev",
			protocol: maximumLibpodAPIVersion, pingRoute: testPodmanPingPath,
			wantPaths: []string{testPodmanPingPath, testPodmanVersionPath, testPodmanMaximumInfoPath},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testPodmanNegotiation(t, test)
		})
	}
}

func testPodmanNegotiation(t *testing.T, test podmanNegotiationTest) {
	t.Helper()
	var requestsLock sync.Mutex
	requests := make([]string, 0, len(test.wantPaths))
	negotiation := podmanNegotiationHandler(test.maximum, test.minimum, test.maximum)
	path := startPodmanTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requestsLock.Lock()
		requests = append(requests, request.URL.Path)
		requestsLock.Unlock()
		if request.URL.Path == testPodmanPingPath && test.pingRoute != testPodmanPingPath {
			http.NotFound(writer, request)

			return
		}
		negotiation.ServeHTTP(writer, request)
	}))
	client, version, err := Connect(context.Background(), path)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	maximumEvidence, _ := parseSemanticVersion(test.maximum)
	wantVersion := Version{
		Protocol: test.protocol, Minimum: test.minimum, Maximum: maximumEvidence.String(),
		Product: libpodAPIVersion, OS: podmanOSLinux, Architecture: podmanArchAMD64,
	}
	apiPath := client.apiPath("/containers/json")
	requestsLock.Lock()
	recordedRequests := slices.Clone(requests)
	requestsLock.Unlock()
	got := podmanNegotiationEvidence{
		Version: version, ClientVersion: client.Version(), ScopePinned: client.scope != (domain.Digest{}),
		PeerPinned: client.peer != (peerIdentity{}), Protocol: client.protocol.String(),
		APIPath: apiPath, Requests: recordedRequests,
	}
	want := podmanNegotiationEvidence{
		Version: wantVersion, ClientVersion: wantVersion, ScopePinned: true, PeerPinned: true,
		Protocol: test.protocol, APIPath: "/v" + test.protocol + "/libpod/containers/json",
		Requests: test.wantPaths,
	}
	assertPodmanNegotiationEvidence(t, got, want)
}

func assertPodmanNegotiationEvidence(t *testing.T, got, want podmanNegotiationEvidence) {
	t.Helper()
	if got.Version != want.Version {
		t.Fatalf("Connect() version = %#v, want %#v", got.Version, want.Version)
	}
	if got.ClientVersion != want.ClientVersion {
		t.Fatalf("Client.Version() = %#v, want %#v", got.ClientVersion, want.ClientVersion)
	}
	if got.ScopePinned != want.ScopePinned || got.PeerPinned != want.PeerPinned {
		t.Fatalf(
			"Connect() pins = scope:%t peer:%t, want scope:%t peer:%t",
			got.ScopePinned,
			got.PeerPinned,
			want.ScopePinned,
			want.PeerPinned,
		)
	}
	if got.Protocol != want.Protocol {
		t.Fatalf("Connect() protocol = %q, want %q", got.Protocol, want.Protocol)
	}
	if got.APIPath != want.APIPath {
		t.Fatalf("Client.apiPath() = %q, want %q", got.APIPath, want.APIPath)
	}
	if !slices.Equal(got.Requests, want.Requests) {
		t.Fatalf("Connect() requests = %v, want %v", got.Requests, want.Requests)
	}
}

func TestConnectRejectsEndpointAndNegotiationFailures(t *testing.T) {
	t.Parallel()

	client, version, err := Connect(context.Background(), "relative.sock")
	if !errors.Is(err, ErrInvalidEndpoint) || client != nil || version != (Version{}) {
		t.Fatalf("Connect(relative) = %#v, %#v, %v", client, version, err)
	}

	tests := []struct {
		name    string
		handler http.Handler
	}{
		{name: "ping status", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusServiceUnavailable)
		})},
		{name: "primary ping method", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusMethodNotAllowed)
		})},
		{name: "ping body", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set(podmanAPIHeader, libpodAPIVersion)
			_, _ = writer.Write([]byte("NO"))
		})},
		{name: "ping header", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte("OK"))
		})},
		{name: "old api", handler: podmanNegotiationHandler("4.3.0", testLibpodServerMinimum, "4.3.0")},
		{name: "future minimum", handler: podmanNegotiationHandler("7.0.0", "7.0.0", "7.0.0")},
		{name: "disagreeing api", handler: podmanNegotiationHandler(libpodAPIVersion, "5.0.0", "6.2.0")},
		{name: "reversed range", handler: podmanNegotiationHandler("5.0.0", "5.1.0", "5.0.0")},
		{name: "invalid version", handler: podmanVersionTestHandler(`{}`)},
		{
			name: "missing engine",
			handler: podmanVersionTestHandler(
				`{"Version":"6.1.0","Components":[{"Name":"other","Version":"6.1.0","Details":{}}]}`,
			),
		},
		{name: "invalid info", handler: podmanTestHandler(func(writer http.ResponseWriter, request *http.Request) {
			if request.URL.Path == libpodPrefix+"/info" {
				writePodmanJSON(writer, `{"host":{"os":"windows","arch":"amd64"},"store":{"graphRoot":"/store"}}`)

				return
			}
			podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion).ServeHTTP(writer, request)
		})},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path := startPodmanTestServer(t, test.handler)
			client, version, err := Connect(context.Background(), path)
			if err == nil || client != nil || version != (Version{}) {
				t.Fatalf("Connect() = %#v, %#v, %v", client, version, err)
			}
		})
	}
}

func TestPingFallbackPropagatesTransportFailure(t *testing.T) {
	t.Parallel()

	client := connectedPodmanImageClient(t, func(http.ResponseWriter, *http.Request) {})
	client.CloseIdleConnections()
	var requests atomic.Int32
	client.httpClient = &http.Client{Transport: podmanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests.Add(1)
		if request.URL.Path == testPodmanPingPath {
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("missing")),
			}, nil
		}

		return nil, errPodmanClientTest
	})}
	if _, err := client.ping(context.Background()); !errors.Is(err, ErrUnavailable) || requests.Load() != 2 {
		t.Fatalf("ping(fallback transport) = %v, requests=%d", err, requests.Load())
	}
}

func TestClientAccessorsAndHTTPHelpersHandleEmptyValues(t *testing.T) {
	t.Parallel()

	if version := (*Client)(nil).Version(); version != (Version{}) {
		t.Fatalf("nil Version() = %#v", version)
	}
	(*Client)(nil).CloseIdleConnections()
	(&Client{}).CloseIdleConnections()
	if effectiveUserID() != uint32(os.Geteuid()) { //nolint:gosec // Unix user IDs use the uint32 uid_t domain.
		t.Fatal("effectiveUserID() changed the native UID")
	}
	client := podmanHTTPClient(&Client{})
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() = %v", err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport = %T", client.Transport)
	}
	if transport.MaxResponseHeaderBytes != maximumHeaderBytes {
		t.Fatalf("MaxResponseHeaderBytes = %d", transport.MaxResponseHeaderBytes)
	}
	closePodmanResponse(nil)
	closePodmanResponse(&http.Response{Body: nil})
}

func TestClientRequestHelpersContainTransportErrors(t *testing.T) {
	t.Parallel()

	response, err := (*Client)(nil).request(
		context.Background(), http.MethodGet, "/", nil, nil, false,
	)
	closePodmanResponse(response)
	if !errors.Is(err, ErrProtocol) || response != nil {
		t.Fatalf("nil request() = %#v, %v", response, err)
	}
	client := failingPodmanHTTPClient()
	request, requestErr := http.NewRequestWithContext(
		context.Background(), http.MethodGet, "http://example.invalid", nil,
	)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	response, err = client.doRequest(context.Background(), request, false)
	closePodmanResponse(response)
	if !errors.Is(err, ErrUnavailable) || response != nil {
		t.Fatalf("doRequest(failure) = %#v, %v", response, err)
	}
}

func TestClientRequestHelpersContainCancellationAndInvalidMethod(t *testing.T) {
	t.Parallel()

	client := failingPodmanHTTPClient()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	request, requestErr := http.NewRequestWithContext(
		cancelled, http.MethodGet, "http://example.invalid", nil,
	)
	if requestErr != nil {
		t.Fatal(requestErr)
	}
	response, err := client.doRequest(cancelled, request, true)
	closePodmanResponse(response)
	if !errors.Is(err, context.Canceled) || response != nil {
		t.Fatalf("doRequest(cancelled) = %#v, %v", response, err)
	}
	request, err = client.newRequest(context.Background(), "bad\nmethod", "/", nil, nil)
	if !errors.Is(err, ErrProtocol) || request != nil {
		t.Fatalf("newRequest(invalid) = %#v, %v", request, err)
	}
}

func failingPodmanHTTPClient() *Client {
	return &Client{
		httpClient: &http.Client{Transport: podmanRoundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errPodmanClientTest
		})},
		baseURL: url.URL{Scheme: "http", Host: podmanDummyHost},
	}
}

func TestClientRequestRejectsChangedSocketIdentity(t *testing.T) {
	t.Parallel()

	path := startPodmanTestServer(t, podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion))
	client, _, err := Connect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	client.socket.inode++
	response, err := client.request(context.Background(), http.MethodGet, testPodmanPingPath, nil, nil, false)
	closePodmanResponse(response)
	if !errors.Is(err, ErrInvalidEndpoint) || response != nil {
		t.Fatalf("request(changed socket) = %#v, %v", response, err)
	}
}

func TestClientRequestRejectsInvalidMethodAfterSocketProof(t *testing.T) {
	t.Parallel()

	client := connectedPodmanImageClient(t, func(http.ResponseWriter, *http.Request) {})
	response, err := client.request(context.Background(), "bad\nmethod", "/", nil, nil, false)
	closePodmanResponse(response)
	if !errors.Is(err, ErrProtocol) || response != nil {
		t.Fatalf("request(invalid method) = %#v, %v", response, err)
	}
}

func TestClientDialAndPeerPinningFailures(t *testing.T) {
	t.Parallel()

	client := &Client{socketPath: filepath.Join(t.TempDir(), "missing.sock")}
	if connection, err := client.dialContext(context.Background(), "", ""); !errors.Is(err, ErrUnavailable) ||
		connection != nil {
		t.Fatalf("dialContext(missing) = %#v, %v", connection, err)
	}
	left, right := net.Pipe()
	defer func() { _ = right.Close() }()
	if connection, err := client.authenticateConnection(left); !errors.Is(err, ErrInvalidEndpoint) ||
		connection != nil {
		t.Fatalf("authenticateConnection(non-unix) = %#v, %v", connection, err)
	}

	path := startPodmanTestServer(t, podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion))
	connection, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	client.peer = peerIdentity{process: -1}
	if authenticated, authErr := client.authenticateConnection(connection); !errors.Is(authErr, ErrInvalidEndpoint) ||
		authenticated != nil {
		t.Fatalf("authenticateConnection(changed peer) = %#v, %v", authenticated, authErr)
	}
}

func TestClientRequestRejectsSocketMutationAfterResponse(t *testing.T) {
	t.Parallel()

	var mutate atomic.Bool
	var path string
	negotiation := podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion)
	path = startPodmanTestServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		negotiation.ServeHTTP(writer, request)
		if mutate.Load() {
			_ = os.Chmod(path, 0o622) //nolint:gosec // The test must mutate the endpoint to an unsafe mode.
		}
	}))
	client, _, err := Connect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	mutate.Store(true)
	response, err := client.request(context.Background(), http.MethodGet, testPodmanPingPath, nil, nil, false)
	closePodmanResponse(response)
	if !errors.Is(err, ErrInvalidEndpoint) || response != nil {
		t.Fatalf("request(mutated socket) = %#v, %v", response, err)
	}
}

func TestInspectScopeRequiresPinnedPeer(t *testing.T) {
	t.Parallel()

	client := connectedPodmanImageClient(t, func(http.ResponseWriter, *http.Request) {})
	version := client.version
	client.peerLock.Lock()
	client.peer = peerIdentity{}
	client.peerLock.Unlock()
	if _, err := client.inspectScope(context.Background(), version); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("inspectScope(empty peer) = %v", err)
	}
}

func TestNegotiationHelpersRejectMalformedEvidence(t *testing.T) {
	t.Parallel()

	client := &Client{}
	if _, err := client.ping(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ping(unready) = %v", err)
	}
	selected, _ := parseSemanticVersion(libpodAPIVersion)
	if _, err := client.serverVersion(
		context.Background(), selected, selected,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("serverVersion(unready) = %v", err)
	}
	if _, err := client.inspectScope(context.Background(), Version{}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("inspectScope(unready) = %v", err)
	}
	engine := versionComponent{Name: "Podman Engine", Version: libpodAPIVersion}
	if _, valid := podmanEngine([]versionComponent{{Name: "other"}}); valid {
		t.Fatal("podmanEngine(missing) accepted")
	}
	if _, valid := podmanEngine([]versionComponent{engine, engine}); valid {
		t.Fatal("podmanEngine(duplicate) accepted")
	}
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{podmanContentType: {podmanJSONType}},
		Body:       io.NopCloser(strings.NewReader(`{"host":{},"store":{}}`)),
	}
	if _, _, err := decodePodmanInfo(response); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanInfo(invalid) = %v", err)
	}
	for _, value := range []string{"", "relative", "/invalid\x00"} {
		if validGraphRoot(value) {
			t.Fatalf("validGraphRoot(%q) = true", value)
		}
	}
}

func TestInspectSocketRejectsUnsafeFilesystemObjects(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	regular := filepath.Join(directory, "regular")
	if err := os.WriteFile(regular, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link")
	if err := os.Symlink(regular, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", directory, regular, link, filepath.Join(directory, "missing")} {
		if _, err := inspectSocket(path); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("inspectSocket(%q) error = %v", path, err)
		}
	}

	socket := startPodmanTestServer(t, podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion))
	if err := os.Chmod(socket, 0o622); err != nil { //nolint:gosec // This test requires an unsafe endpoint mode.
		t.Fatal(err)
	}
	if _, err := inspectSocket(socket); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("inspectSocket(writable) error = %v", err)
	}
}

//nolint:cyclop // The assertions cover independent semantic-version branches.
func TestSemanticVersionHelpers(t *testing.T) {
	t.Parallel()

	versions := []string{
		"", "1", "1.2", "1.2.3.4", "01.2.3", "1.-2.3", "x.2.3",
		"1.2.3-", "1.2.3-rc..1", "1.2.3-01", "1.2.3-rc+build",
	}
	for _, value := range versions {
		if validSemanticVersion(value) {
			t.Fatalf("validSemanticVersion(%q) = true", value)
		}
	}
	for _, value := range []string{"6.2.0-dev", "5.6.0-rc1", "5.6.0-rc.1", "5.6.0-RC-1"} {
		version, valid := parseSemanticVersion(value)
		if !valid || version.String() != strings.Split(value, "-")[0] {
			t.Fatalf("parseSemanticVersion(%q) = %#v, %t", value, version, valid)
		}
	}
	minimum, _ := parseSemanticVersion(minimumLibpodAPIVersion)
	middle, _ := parseSemanticVersion(testLibpodMiddleVersion)
	maximum, _ := parseSemanticVersion(maximumLibpodAPIVersion)
	future, _ := parseSemanticVersion("7.0.0")
	old, _ := parseSemanticVersion("4.3.0")
	if selected, valid := compatibleLibpodVersion(minimum); !valid || selected != minimum {
		t.Fatalf("compatibleLibpodVersion(minimum) = %#v, %t", selected, valid)
	}
	if selected, valid := compatibleLibpodVersion(middle); !valid || selected != middle {
		t.Fatalf("compatibleLibpodVersion(middle) = %#v, %t", selected, valid)
	}
	if selected, valid := compatibleLibpodVersion(future); !valid || selected != maximum {
		t.Fatalf("compatibleLibpodVersion(future) = %#v, %t", selected, valid)
	}
	if _, valid := compatibleLibpodVersion(old); valid {
		t.Fatal("compatibleLibpodVersion(old) accepted")
	}
	if !(semanticVersion{major: 1}).less(semanticVersion{major: 2}) ||
		!(semanticVersion{major: 1, minor: 1}).less(semanticVersion{major: 1, minor: 2}) ||
		!(semanticVersion{major: 1, minor: 1, patch: 1}).less(semanticVersion{major: 1, minor: 1, patch: 2}) ||
		(semanticVersion{major: 2}).less(semanticVersion{major: 1}) {
		t.Fatal("semanticVersion.less() returned an invalid result")
	}
	if middle.String() != testLibpodMiddleVersion {
		t.Fatalf("semanticVersion.String() = %q", middle.String())
	}
}

func TestPodmanTextAndJSONHelpers(t *testing.T) {
	t.Parallel()

	if validPodmanText("") || validPodmanText("x\x00") ||
		validPodmanText(strings.Repeat("x", maximumTextBytes+1)) {
		t.Fatal("validPodmanText() accepted malformed text")
	}
	if isPodmanJSON("text/plain") || !isPodmanJSON(podmanJSONType+"; charset=utf-8") {
		t.Fatal("isPodmanJSON() returned an invalid result")
	}
	var value map[string]bool
	if decodePodmanJSON(strings.NewReader(`{"x":true,"x":false}`), 64, &value) ||
		decodePodmanJSON(strings.NewReader(`{"x":true}`), 2, &value) ||
		!decodePodmanJSON(strings.NewReader(`{"x":true}`), 64, &value) || !value["x"] {
		t.Fatal("decodePodmanJSON() returned an invalid result")
	}
}

func TestPodmanPlatformHelpers(t *testing.T) {
	t.Parallel()

	if platform, valid := podmanPlatform(podmanOSLinux, podmanArchARM64); !valid || platform.Variant != "v8" {
		t.Fatalf("podmanPlatform(arm64) = %#v, %t", platform, valid)
	}
	for _, values := range [][2]string{{"darwin", podmanArchAMD64}, {podmanOSLinux, "386"}} {
		if _, valid := podmanPlatform(values[0], values[1]); valid {
			t.Fatalf("podmanPlatform(%q, %q) accepted", values[0], values[1])
		}
	}
}

func podmanNegotiationHandler(ping, minimum, maximum string) http.Handler {
	serverMaximum, _ := parseSemanticVersion(maximum)
	selected, _ := compatibleLibpodVersion(serverMaximum)
	infoPath := "/v" + selected.String() + "/libpod/info"

	return podmanTestHandler(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case testPodmanPingPath, testPodmanFallbackPingPath:
			writer.Header().Set(podmanAPIHeader, ping)
			_, _ = writer.Write([]byte("OK"))
		case testPodmanVersionPath:
			writePodmanJSON(writer, fmt.Sprintf(
				`{"Version":"6.1.0","Components":[{"Name":"Podman Engine",`+
					`"Version":"6.1.0","Details":{"APIVersion":%q,"MinAPIVersion":%q}}]}`,
				maximum,
				minimum,
			))
		case infoPath:
			writePodmanJSON(writer,
				`{"host":{"os":"linux","arch":"amd64"},`+
					`"store":{"graphRoot":"/var/lib/containers/storage"}}`,
			)
		default:
			http.NotFound(writer, request)
		}
	})
}

func podmanVersionTestHandler(versionDocument string) http.Handler {
	negotiation := podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion)

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == testPodmanVersionPath {
			writePodmanJSON(writer, versionDocument)

			return
		}
		negotiation.ServeHTTP(writer, request)
	})
}

func podmanTestHandler(handler http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		handler(writer, request)
	})
}

func writePodmanJSON(writer http.ResponseWriter, value string) {
	writer.Header().Set(podmanContentType, podmanJSONType)
	_, _ = writer.Write([]byte(value))
}

func startPodmanTestServer(t *testing.T, handler http.Handler) string {
	t.Helper()

	directory, err := os.MkdirTemp("/tmp", "maniud-") //nolint:usetesting // Darwin test paths must fit sockaddr_un.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "podman.sock")
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // Unix sockets require owner access in tests.
		_ = listener.Close()
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler} //nolint:gosec // Tests bind only an authenticated local Unix socket.
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	return path
}
