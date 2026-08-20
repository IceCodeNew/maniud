package docker

import (
	"crypto/tls"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

const testTLSEngineAddress = "https://engine.example:2376"

func TestUnixEndpointValidation(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"docker.sock", "/tmp/../tmp/docker.sock", "/tmp/docker\x00.sock"} {
		_, err := UnixEndpoint(value)
		if !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("UnixEndpoint(%q) error = %v, want ErrInvalidEndpoint", value, err)
		}
	}

	endpoint, err := UnixEndpoint("/tmp/docker.sock")
	if err != nil {
		t.Fatalf("UnixEndpoint() error = %v", err)
	}

	if endpoint.baseURL.String() != "http://docker.invalid" || endpoint.transport.Proxy != nil ||
		!endpoint.transport.DisableCompression {
		t.Fatalf("UnixEndpoint() = %#v", endpoint)
	}
}

func TestTLSEndpointValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		address string
		config  *tls.Config
	}{
		{name: "plain scheme", address: "http://engine.example:2376", config: nil},
		{name: "tcp scheme", address: "tcp://engine.example:2376", config: nil},
		{name: "missing port", address: "https://engine.example", config: nil},
		{name: "empty host", address: "https://:2376", config: nil},
		{name: "zero port", address: "https://engine.example:0", config: nil},
		{name: "large port", address: "https://engine.example:65536", config: nil},
		{name: "named port", address: "https://engine.example:docker", config: nil},
		{name: "userinfo", address: "https://user@engine.example:2376", config: nil},
		{name: "path", address: "https://engine.example:2376/api", config: nil},
		{name: "query", address: "https://engine.example:2376?x=1", config: nil},
		{name: "fragment", address: "https://engine.example:2376#x", config: nil},
		{
			name:    "verification disabled",
			address: testTLSEngineAddress,
			config: &tls.Config{ //nolint:exhaustruct,gosec // Invalid input fixture disables verification intentionally.
				InsecureSkipVerify: true,
			},
		},
		{
			name:    "old minimum",
			address: testTLSEngineAddress,
			config: &tls.Config{ //nolint:exhaustruct,gosec // Invalid input fixture uses a retired TLS version.
				MinVersion: tls.VersionTLS11,
			},
		},
		{
			name:    "old maximum",
			address: testTLSEngineAddress,
			config: &tls.Config{ //nolint:exhaustruct,gosec // Invalid input fixture uses a retired TLS version.
				MaxVersion: tls.VersionTLS11,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := TLSEndpoint(test.address, test.config)
			if !errors.Is(err, ErrInvalidEndpoint) {
				t.Fatalf("TLSEndpoint() error = %v, want ErrInvalidEndpoint", err)
			}
		})
	}
}

func TestTLSEndpointClonesSecureConfig(t *testing.T) {
	t.Parallel()

	config := &tls.Config{ //nolint:exhaustruct // Optional TLS fields retain Go's secure defaults.
		MinVersion: tls.VersionTLS13,
		ServerName: "engine.example",
	}

	endpoint, err := TLSEndpoint(testTLSEngineAddress, config)
	if err != nil {
		t.Fatalf("TLSEndpoint() error = %v", err)
	}

	config.ServerName = "changed.example"
	if endpoint.transport.TLSClientConfig == config ||
		endpoint.transport.TLSClientConfig.ServerName != "engine.example" || endpoint.transport.Proxy != nil {
		t.Fatalf("TLSEndpoint() TLS config = %#v", endpoint.transport.TLSClientConfig)
	}

	defaultEndpoint, err := TLSEndpoint(testTLSEngineAddress, nil)
	if err != nil {
		t.Fatalf("TLSEndpoint(default) error = %v", err)
	}

	if defaultEndpoint.transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLSEndpoint(default) minimum TLS = %d", defaultEndpoint.transport.TLSClientConfig.MinVersion)
	}
}

func TestVPNEndpointRequiresDeliveredWarning(t *testing.T) {
	t.Parallel()

	_, err := VPNEndpoint("tcp://engine.example:2375", nil)
	if !errors.Is(err, ErrWarningDelivery) {
		t.Fatalf("VPNEndpoint(nil sink) error = %v, want ErrWarningDelivery", err)
	}

	_, err = VPNEndpoint("tcp://engine.example:2375", func(Warning) error {
		return io.ErrClosedPipe
	})
	if !errors.Is(err, ErrWarningDelivery) {
		t.Fatalf("VPNEndpoint(failing sink) error = %v, want ErrWarningDelivery", err)
	}

	called := false

	_, err = VPNEndpoint("http://engine.example:2375", func(Warning) error {
		called = true

		return nil
	})
	if !errors.Is(err, ErrInvalidEndpoint) || called {
		t.Fatalf("VPNEndpoint(invalid) error = %v, warning called = %t", err, called)
	}

	var warning Warning

	endpoint, err := VPNEndpoint("tcp://engine.example:2375", func(value Warning) error {
		warning = value

		return nil
	})
	if err != nil {
		t.Fatalf("VPNEndpoint() error = %v", err)
	}

	if warning.Code != WarningInsecureRemoteEngine ||
		!strings.Contains(warning.Message, "host-root control") ||
		endpoint.baseURL.String() != "http://engine.example:2375" || endpoint.transport.Proxy != nil {
		t.Fatalf("VPNEndpoint() warning = %#v, endpoint = %#v", warning, endpoint)
	}
}

func TestVPNEndpointAcceptsIPv6(t *testing.T) {
	t.Parallel()

	warned := false

	endpoint, err := VPNEndpoint("tcp://[fd7a:115c:a1e0::1]:2375", func(Warning) error {
		warned = true

		return nil
	})
	if err != nil {
		t.Fatalf("VPNEndpoint(IPv6) error = %v", err)
	}

	if !warned || endpoint.baseURL.String() != "http://[fd7a:115c:a1e0::1]:2375" {
		t.Fatalf("VPNEndpoint(IPv6) warned = %t, URL = %q", warned, endpoint.baseURL.String())
	}
}

func TestBaseTransportSecurityDefaults(t *testing.T) {
	t.Parallel()

	transport := baseTransport()
	if !transportOwnsNetworkPath(transport) || !transportHasResourceLimits(transport) {
		t.Fatalf("baseTransport() = %#v", transport)
	}

	if http.DefaultTransport == transport {
		t.Fatal("baseTransport() returned the process-global transport")
	}
}

func transportOwnsNetworkPath(transport *http.Transport) bool {
	return transport.Proxy == nil && !transport.ForceAttemptHTTP2 && transport.DialContext != nil
}

func transportHasResourceLimits(transport *http.Transport) bool {
	return transport.MaxIdleConns == maximumIdleConns && transport.ResponseHeaderTimeout == headerTimeout &&
		transport.TLSHandshakeTimeout == tlsHandshakeLimit && transport.IdleConnTimeout == idleTimeout &&
		transport.ExpectContinueTimeout == time.Second && transport.MaxResponseHeaderBytes == maximumHeaderSize
}
