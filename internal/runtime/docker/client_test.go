package docker

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestConnectNegotiatesSupportedVersions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		serverMaximum string
		wantProtocol  string
	}{
		{serverMaximum: minimumAPIVersion, wantProtocol: minimumAPIVersion},
		{serverMaximum: "1.55", wantProtocol: maximumAPIVersion},
		{serverMaximum: "1.56", wantProtocol: maximumAPIVersion},
	}

	for _, test := range tests {
		t.Run(test.serverMaximum, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32

			fixture := validEngineFixture(test.serverMaximum)
			fixture.onRequest = func(request *http.Request) {
				assertNegotiationRequest(t, request, requests.Add(1), test.wantProtocol)
			}
			server := httptest.NewServer(engineHandler(t, fixture))
			t.Cleanup(server.Close)

			endpoint := testVPNEndpoint(t, server.URL, func(Warning) error { return nil })

			client, got, err := Connect(context.Background(), endpoint)
			if err != nil {
				t.Fatalf("Connect() error = %v", err)
			}

			t.Cleanup(client.CloseIdleConnections)

			want := Version{
				Protocol:     test.wantProtocol,
				Minimum:      "1.40",
				Maximum:      test.serverMaximum,
				Product:      testProduct,
				OS:           testOS,
				Architecture: testArchitecture,
			}
			if got != want || client.Version() != want || requests.Load() != 2 ||
				client.httpClient.Timeout != requestTimeout {
				t.Fatalf(
					"Connect() = %#v, Version() = %#v, requests = %d; want %#v, 2",
					got, client.Version(), requests.Load(), want,
				)
			}
		})
	}

	var zero Client
	zero.CloseIdleConnections()
}

func assertNegotiationRequest(t *testing.T, request *http.Request, requestNumber int32, protocol string) {
	t.Helper()

	if request.Header.Get("Accept") != jsonContentType {
		t.Errorf("Accept = %q", request.Header.Get("Accept"))
	}

	switch requestNumber {
	case 1:
		if request.Method != http.MethodHead || request.URL.Path != "/_ping" {
			t.Errorf("first request = %s %s", request.Method, request.URL.Path)
		}
	case 2:
		if request.Method != http.MethodGet || request.URL.Path != "/v"+protocol+"/version" {
			t.Errorf("second request = %s %s", request.Method, request.URL.Path)
		}
	}
}

func TestVPNWarningPrecedesNetwork(t *testing.T) {
	t.Parallel()

	var warned atomic.Bool

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !warned.Load() {
			t.Error("request arrived before warning")
		}

		engineHandler(t, validEngineFixture(maximumAPIVersion)).ServeHTTP(response, request)
	}))
	t.Cleanup(server.Close)

	warningCount := 0
	endpoint := testVPNEndpoint(t, server.URL, func(warning Warning) error {
		warningCount++

		if warning.Code != WarningInsecureRemoteEngine {
			t.Fatalf("warning code = %q", warning.Code)
		}

		warned.Store(true)

		return nil
	})

	client, _, err := Connect(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	client.CloseIdleConnections()

	if warningCount != 1 {
		t.Fatalf("warning count = %d, want 1", warningCount)
	}
}

func TestConnectUsesUnixSocket(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "docker.sock")

	var listenConfig net.ListenConfig

	listener, err := listenConfig.Listen(context.Background(), "unix", socketPath)
	if err != nil {
		t.Fatalf("net.Listen(unix) error = %v", err)
	}

	fixture := validEngineFixture(minimumAPIVersion)
	fixture.version = versionDocument(minimumAPIVersion, "1.40", "29.4.0", "linux", "arm64")
	server := &http.Server{ //nolint:exhaustruct // The test server does not need optional production hooks.
		Handler:           engineHandler(t, fixture),
		ReadHeaderTimeout: headerTimeout,
	}

	done := make(chan error, 1)
	go func() {
		done <- server.Serve(listener)
	}()

	t.Cleanup(func() {
		_ = server.Close()

		<-done
	})

	endpoint, err := UnixEndpoint(socketPath)
	if err != nil {
		t.Fatalf("UnixEndpoint() error = %v", err)
	}

	client, version, err := Connect(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	client.CloseIdleConnections()

	if version.Protocol != minimumAPIVersion || version.Architecture != "arm64" {
		t.Fatalf("Connect() version = %#v", version)
	}
}

func TestConnectUsesVerifiedMTLS(t *testing.T) {
	t.Parallel()

	peerCertificateSeen := atomic.Bool{}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.TLS != nil && len(request.TLS.PeerCertificates) > 0 {
			peerCertificateSeen.Store(true)
		}

		engineHandler(t, validEngineFixture(maximumAPIVersion)).ServeHTTP(response, request)
	}))
	server.TLS = &tls.Config{ //nolint:exhaustruct // The test server only needs its mTLS policy.
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.RequireAnyClientCert,
	}
	server.StartTLS()
	t.Cleanup(server.Close)

	serverClient, ok := server.Client().Transport.(*http.Transport)
	if !ok || serverClient.TLSClientConfig == nil {
		t.Fatal("httptest TLS transport is unavailable")
	}

	config := serverClient.TLSClientConfig.Clone()
	config.Certificates = []tls.Certificate{server.TLS.Certificates[0]}

	endpoint, err := TLSEndpoint(server.URL, config)
	if err != nil {
		t.Fatalf("TLSEndpoint() error = %v", err)
	}

	client, _, err := Connect(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	client.CloseIdleConnections()

	if !peerCertificateSeen.Load() {
		t.Fatal("server did not receive the configured client certificate")
	}
}

func TestConnectRejectsUnverifiedTLS(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(engineHandler(t, validEngineFixture(maximumAPIVersion)))
	t.Cleanup(server.Close)

	endpoint, err := TLSEndpoint(server.URL, nil)
	if err != nil {
		t.Fatalf("TLSEndpoint() error = %v", err)
	}

	client, version, err := Connect(context.Background(), endpoint)

	var emptyVersion Version

	if client != nil || version != emptyVersion || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Connect(unverified TLS) = %#v, %#v, %v; want nil, zero, ErrUnavailable", client, version, err)
	}
}

func TestConnectContainsTransportErrors(t *testing.T) {
	t.Parallel()

	endpoint, err := VPNEndpoint("tcp://127.0.0.1:1", func(Warning) error { return nil })
	if err != nil {
		t.Fatalf("VPNEndpoint() error = %v", err)
	}

	_, _, err = Connect(context.Background(), endpoint)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Connect(unavailable) error = %v, want ErrUnavailable", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = Connect(ctx, endpoint)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Connect(cancelled) error = %v, want context.Canceled", err)
	}

	var emptyEndpoint Endpoint

	_, _, err = Connect(context.Background(), emptyEndpoint)
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("Connect(zero endpoint) error = %v, want ErrInvalidEndpoint", err)
	}
}

func TestConnectRejectsRedirect(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "/redirected", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(server.Close)

	endpoint := testVPNEndpoint(t, server.URL, func(Warning) error { return nil })
	client, version, err := Connect(context.Background(), endpoint)

	var emptyVersion Version

	if client != nil || version != emptyVersion || !errors.Is(err, ErrProtocol) {
		t.Fatalf("Connect(redirect) = %#v, %#v, %v; want nil, zero, ErrProtocol", client, version, err)
	}
}
