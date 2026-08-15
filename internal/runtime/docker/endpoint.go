package docker

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	// WarningInsecureRemoteEngine identifies warning-gated plain TCP access.
	WarningInsecureRemoteEngine WarningCode = "insecure_remote_engine"
	insecureRemoteEngineMessage             = "remote Docker Engine uses unauthenticated plain TCP; " +
		"Engine API access usually grants host-root control; bind it only to a controlled VPN interface and firewall"
)

var (
	// ErrInvalidEndpoint reports a Docker transport outside the supported allowlist.
	ErrInvalidEndpoint = errors.New("docker endpoint is invalid")
	// ErrWarningDelivery reports that the required plain TCP warning did not reach its sink.
	ErrWarningDelivery = errors.New("docker endpoint warning delivery failed")
)

// WarningCode is a stable machine-readable Docker warning identity.
type WarningCode string

// Warning is emitted before using a warning-gated Docker endpoint.
type Warning struct {
	Code    WarningCode
	Message string
}

// WarningSink delivers a warning before the adapter can issue a request.
type WarningSink func(Warning) error

// Endpoint is one validated Docker transport. Its fields remain private so callers
// cannot construct unsupported endpoint combinations.
type Endpoint struct {
	baseURL   url.URL
	transport *http.Transport
}

// UnixEndpoint creates an HTTP-over-Unix-socket endpoint.
func UnixEndpoint(socketPath string) (Endpoint, error) {
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath ||
		strings.ContainsRune(socketPath, '\x00') {
		return Endpoint{}, ErrInvalidEndpoint
	}

	transport := baseTransport()
	transport.DisableCompression = true
	transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
		dialer := &net.Dialer{ //nolint:exhaustruct // Zero values retain the standard dialer policy for optional fields.
			Timeout:   dialTimeout,
			KeepAlive: keepAlive,
		}

		return dialer.DialContext(ctx, "unix", socketPath)
	}
	baseURL := url.URL{ //nolint:exhaustruct // A request base intentionally has no path, query, user, or fragment.
		Scheme: httpScheme,
		Host:   "docker.invalid",
	}

	return Endpoint{
		baseURL:   baseURL,
		transport: transport,
	}, nil
}

// TLSEndpoint creates a server-authenticated HTTPS endpoint. A client certificate
// in config enables mTLS.
func TLSEndpoint(address string, config *tls.Config) (Endpoint, error) {
	baseURL, err := parseNetworkEndpoint(address, "https", "https")
	if err != nil {
		return Endpoint{}, err
	}

	tlsConfig := &tls.Config{ //nolint:exhaustruct // Go supplies secure defaults for fields the caller did not configure.
		MinVersion: tls.VersionTLS12,
	}
	if config != nil {
		tlsConfig = config.Clone()
		if tlsConfig.InsecureSkipVerify ||
			tlsConfig.MinVersion != 0 && tlsConfig.MinVersion < tls.VersionTLS12 ||
			tlsConfig.MaxVersion != 0 && tlsConfig.MaxVersion < tls.VersionTLS12 {
			return Endpoint{}, ErrInvalidEndpoint
		}

		if tlsConfig.MinVersion == 0 {
			tlsConfig.MinVersion = tls.VersionTLS12
		}
	}

	transport := baseTransport()
	transport.TLSClientConfig = tlsConfig

	return Endpoint{baseURL: baseURL, transport: transport}, nil
}

// VPNEndpoint creates plain TCP access after delivering the mandatory warning.
// Calling this constructor is the user's explicit declaration that a VPN and
// firewall protect the endpoint.
func VPNEndpoint(address string, warningSink WarningSink) (Endpoint, error) {
	baseURL, err := parseNetworkEndpoint(address, "tcp", httpScheme)
	if err != nil {
		return Endpoint{}, err
	}

	if warningSink == nil {
		return Endpoint{}, ErrWarningDelivery
	}

	err = warningSink(Warning{Code: WarningInsecureRemoteEngine, Message: insecureRemoteEngineMessage})
	if err != nil {
		return Endpoint{}, ErrWarningDelivery
	}

	return Endpoint{baseURL: baseURL, transport: baseTransport()}, nil
}

const (
	dialTimeout       = 10 * time.Second
	keepAlive         = 30 * time.Second
	headerTimeout     = 15 * time.Second
	tlsHandshakeLimit = 10 * time.Second
	idleTimeout       = 30 * time.Second
	requestTimeout    = 30 * time.Second
	maximumIdleConns  = 6
	maximumHeaderSize = 64 << 10
)

func baseTransport() *http.Transport {
	dialer := &net.Dialer{ //nolint:exhaustruct // Zero values retain the standard dialer policy for optional fields.
		Timeout:   dialTimeout,
		KeepAlive: keepAlive,
	}

	return &http.Transport{ //nolint:exhaustruct // Unsupported hooks stay nil so the adapter owns every network path.
		Proxy:                  nil,
		DialContext:            dialer.DialContext,
		ForceAttemptHTTP2:      false,
		MaxIdleConns:           maximumIdleConns,
		IdleConnTimeout:        idleTimeout,
		TLSHandshakeTimeout:    tlsHandshakeLimit,
		ResponseHeaderTimeout:  headerTimeout,
		ExpectContinueTimeout:  time.Second,
		MaxResponseHeaderBytes: maximumHeaderSize,
	}
}

func parseNetworkEndpoint(address, inputScheme, requestScheme string) (url.URL, error) {
	parsed, err := url.Parse(address)
	if err != nil || !validNetworkAuthority(parsed, inputScheme) || !emptyNetworkResource(parsed) {
		return url.URL{}, ErrInvalidEndpoint
	}

	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || host == "" {
		return url.URL{}, ErrInvalidEndpoint
	}

	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return url.URL{}, ErrInvalidEndpoint
	}

	parsed.Scheme = requestScheme

	return *parsed, nil
}

func validNetworkAuthority(parsed *url.URL, scheme string) bool {
	return parsed.Scheme == scheme && parsed.Opaque == "" && parsed.User == nil
}

func emptyNetworkResource(parsed *url.URL) bool {
	return parsed.Path == "" && parsed.RawPath == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}
