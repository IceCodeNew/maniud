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
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

var errPodmanClientTest = errors.New("podman client test failure")

type podmanRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip podmanRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func TestConnectPinsNativeLibpodScope(t *testing.T) {
	t.Parallel()

	path := startPodmanTestServer(t, podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion))
	client, version, err := Connect(context.Background(), path)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	if version != (Version{
		Protocol: libpodAPIVersion, Minimum: "5.0.0", Maximum: libpodAPIVersion,
		Product: libpodAPIVersion, OS: podmanOSLinux, Architecture: podmanArchAMD64,
	}) || client.Version() != version || client.scope == (domain.Digest{}) ||
		client.peer == (peerIdentity{}) {
		t.Fatalf("Connect() = %#v, scope=%v, peer=%#v", version, client.scope, client.peer)
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
		{name: "ping body", handler: http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set(podmanAPIHeader, libpodAPIVersion)
			_, _ = writer.Write([]byte("NO"))
		})},
		{name: "old api", handler: podmanNegotiationHandler("6.0.0", "4.0.0", "6.0.0")},
		{name: "future minimum", handler: podmanNegotiationHandler("7.0.0", "7.0.0", "7.0.0")},
		{name: "disagreeing api", handler: podmanNegotiationHandler(libpodAPIVersion, "5.0.0", "6.2.0")},
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
	response, err := client.request(context.Background(), http.MethodGet, "/_ping", nil, nil, false)
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
	response, err := client.request(context.Background(), http.MethodGet, "/_ping", nil, nil, false)
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
	if _, err := client.serverVersion(context.Background(), libpodAPIVersion); !errors.Is(err, ErrProtocol) {
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

func TestSemanticVersionHelpers(t *testing.T) {
	t.Parallel()

	versions := []string{"", "1", "1.2", "1.2.3.4", "01.2.3", "1.-2.3", "x.2.3"}
	for _, value := range versions {
		if validSemanticVersion(value) {
			t.Fatalf("validSemanticVersion(%q) = true", value)
		}
	}
	if !compatibleLibpodRange("6.1.0", "6.1.0") ||
		compatibleLibpodRange("6.1.1", "7.0.0") || compatibleLibpodRange("4.0.0", "6.0.9") {
		t.Fatal("compatibleLibpodRange() returned an invalid result")
	}
	if !(semanticVersion{major: 1}).less(semanticVersion{major: 2}) ||
		!(semanticVersion{major: 1, minor: 1}).less(semanticVersion{major: 1, minor: 2}) ||
		!(semanticVersion{major: 1, minor: 1, patch: 1}).less(semanticVersion{major: 1, minor: 1, patch: 2}) ||
		(semanticVersion{major: 2}).less(semanticVersion{major: 1}) {
		t.Fatal("semanticVersion.less() returned an invalid result")
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
	return podmanTestHandler(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_ping":
			writer.Header().Set(podmanAPIHeader, ping)
			_, _ = writer.Write([]byte("OK"))
		case "/version":
			writePodmanJSON(writer, fmt.Sprintf(
				`{"Version":"6.1.0","Components":[{"Name":"Podman Engine",`+
					`"Version":"6.1.0","Details":{"APIVersion":%q,"MinAPIVersion":%q}}]}`,
				maximum,
				minimum,
			))
		case libpodPrefix + "/info":
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
		if request.URL.Path == "/version" {
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
