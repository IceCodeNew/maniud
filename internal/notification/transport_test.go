package notification

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testNotificationHost = "notify.example"
	testTCPNetwork       = "tcp"
)

var errTestTransport = errors.New("test transport failure")

type notificationResolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (resolve notificationResolverFunc) LookupNetIP(
	ctx context.Context,
	network string,
	host string,
) ([]netip.Addr, error) {
	return resolve(ctx, network, host)
}

func TestNotificationHTTPClientUsesBoundedFixedHostPolicy(t *testing.T) {
	t.Parallel()

	client := newNotificationHTTPClientWith(
		testNotificationHost,
		notificationResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}, nil
		}),
		func(context.Context, string, string) (net.Conn, error) { return testPipe(t), nil },
	)
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("notification client transport = %T", client.Transport)
	}
	want := notificationTransportPolicy{
		clientTimeout: notificationRequestTimeout, redirect: true, direct: true, dial: true, noTLSDial: true,
		minimumTLS: tls.VersionTLS12, serverName: testNotificationHost, compressionDisabled: true,
		maximumIdle: notificationMaximumConnections, maximumIdlePerHost: 1,
		maximumConnections: notificationMaximumConnections, idleTimeout: notificationIdleTimeout,
		responseTimeout: notificationRequestTimeout, maximumHeaders: notificationMaximumHeaderBytes,
		http1: true, http2: true, noUnencryptedHTTP2: true,
	}
	if got := observedNotificationTransportPolicy(client, transport); got != want {
		t.Fatalf("notification client policy = %#v, want %#v", got, want)
	}
	if err := client.CheckRedirect(nil, nil); !errors.Is(err, errTransportPolicy) {
		t.Fatalf("redirect policy error = %v", err)
	}

	production := newNotificationHTTPClient(testNotificationHost)
	if production.Timeout != notificationRequestTimeout || production.Transport == nil {
		t.Fatalf("production notification client = %#v", production)
	}
}

func TestFixedHostDialPinsOneValidatedResolution(t *testing.T) {
	t.Parallel()

	wantAddresses := []netip.Addr{
		netip.MustParseAddr("1.1.1.1"),
		netip.MustParseAddr("2606:4700:4700::1111"),
	}
	resolveCalls := 0
	var lookupNetwork string
	resolver := notificationResolverFunc(func(_ context.Context, network, host string) ([]netip.Addr, error) {
		resolveCalls++
		lookupNetwork = network
		if host != testNotificationHost {
			t.Fatalf("resolved host = %q", host)
		}

		return slices.Clone(wantAddresses), nil
	})
	var dialed []string
	dial := fixedHostDialContext(testNotificationHost, resolver, func(
		_ context.Context,
		network string,
		address string,
	) (net.Conn, error) {
		if network != testTCPNetwork {
			t.Fatalf("dial network = %q", network)
		}
		dialed = append(dialed, address)
		if len(dialed) == 1 {
			return nil, errTestTransport
		}

		return testPipe(t), nil
	})

	connection, err := dial(t.Context(), testTCPNetwork, testNotificationHost+":"+notificationHTTPSPort)
	if err != nil {
		t.Fatalf("fixed host dial error = %v", err)
	}
	if closeErr := connection.Close(); closeErr != nil {
		t.Fatalf("close fixed host connection: %v", closeErr)
	}
	wantDialed := []string{"1.1.1.1:443", "[2606:4700:4700::1111]:443"}
	if resolveCalls != 1 || lookupNetwork != "ip" || !slices.Equal(dialed, wantDialed) {
		t.Fatalf("fixed host dial resolved %d via %q and dialed %q", resolveCalls, lookupNetwork, dialed)
	}
}

func TestFixedHostDialRejectsUntrustedResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		addresses []netip.Addr
	}{
		{name: "empty"},
		{name: "invalid", addresses: []netip.Addr{{}}},
		{name: "unspecified", addresses: notificationAddresses("0.0.0.0")},
		{name: "loopback", addresses: notificationAddresses("127.0.0.1")},
		{name: "link local", addresses: notificationAddresses("169.254.1.1")},
		{name: "private", addresses: notificationAddresses("10.0.0.1")},
		{name: "shared", addresses: notificationAddresses("100.64.0.1")},
		{name: "benchmark", addresses: notificationAddresses("198.18.0.1")},
		{name: "documentation", addresses: notificationAddresses("203.0.113.1")},
		{name: "reserved", addresses: notificationAddresses("240.0.0.1")},
		{name: "multicast", addresses: notificationAddresses("224.0.0.1")},
		{name: "IPv6 loopback", addresses: notificationAddresses("::1")},
		{name: "IPv6 link local", addresses: notificationAddresses("fe80::1")},
		{name: "IPv6 ULA", addresses: notificationAddresses("fd00::1")},
		{name: "IPv6 documentation", addresses: notificationAddresses("2001:db8::1")},
		{name: "IPv6 zone", addresses: notificationAddresses("fe80::1%notification")},
		{name: "mapped private", addresses: notificationAddresses("::ffff:192.168.1.1")},
		{
			name:      "mixed public private",
			addresses: notificationAddresses("1.1.1.1", "192.168.1.1"),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dialCalls := 0
			dial := fixedHostDialContext(
				testNotificationHost,
				notificationResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
					return slices.Clone(test.addresses), nil
				}),
				func(context.Context, string, string) (net.Conn, error) {
					dialCalls++

					return testPipe(t), nil
				},
			)

			connection, err := dial(t.Context(), testTCPNetwork, testNotificationHost+":"+notificationHTTPSPort)
			if connection != nil || !errors.Is(err, errTransportPolicy) || dialCalls != 0 {
				t.Fatalf("untrusted dial = %v, %v, calls %d", connection, err, dialCalls)
			}
		})
	}
}

func TestFixedHostDialRejectsOtherDestinations(t *testing.T) {
	t.Parallel()

	resolveCalls := 0
	dialCalls := 0
	dial := fixedHostDialContext(
		testNotificationHost,
		notificationResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			resolveCalls++

			return notificationAddresses("1.1.1.1"), nil
		}),
		func(context.Context, string, string) (net.Conn, error) {
			dialCalls++

			return testPipe(t), nil
		},
	)
	for _, request := range []struct {
		network string
		address string
	}{
		{network: "udp", address: testNotificationHost + ":443"},
		{network: testTCPNetwork, address: "other.example:443"},
		{network: testTCPNetwork, address: testNotificationHost + ":80"},
		{network: testTCPNetwork, address: testNotificationHost},
	} {
		connection, err := dial(t.Context(), request.network, request.address)
		if connection != nil || !errors.Is(err, errTransportPolicy) {
			t.Fatalf("fixed host dial(%q, %q) = %v, %v", request.network, request.address, connection, err)
		}
	}
	if resolveCalls != 0 || dialCalls != 0 {
		t.Fatalf("rejected destination resolved %d times and dialed %d times", resolveCalls, dialCalls)
	}
}

func TestFixedHostDialMapsResolverAndDialFailures(t *testing.T) {
	t.Parallel()

	resolverFailure := fixedHostDialContext(
		testNotificationHost,
		notificationResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, errTestTransport
		}),
		func(context.Context, string, string) (net.Conn, error) { return testPipe(t), nil },
	)
	if connection, err := resolverFailure(
		t.Context(), "tcp4", testNotificationHost+":443",
	); connection != nil || !errors.Is(err, errTransportUnavailable) || stringsContain(err, testNotificationHost) {
		t.Fatalf("resolver failure = %v, %v", connection, err)
	}

	dialFailure := fixedHostDialContext(
		testNotificationHost,
		notificationResolverFunc(func(_ context.Context, network, _ string) ([]netip.Addr, error) {
			if network != "ip6" {
				t.Fatalf("lookup network = %q", network)
			}

			return notificationAddresses("2606:4700:4700::1111"), nil
		}),
		func(context.Context, string, string) (net.Conn, error) { return nil, errTestTransport },
	)
	if connection, err := dialFailure(
		t.Context(), "tcp6", testNotificationHost+":443",
	); connection != nil || !errors.Is(err, errTransportUnavailable) || errors.Is(err, errTestTransport) {
		t.Fatalf("dial failure = %v, %v", connection, err)
	}
}

func TestFixedHostDialPreservesCancellation(t *testing.T) {
	t.Parallel()

	resolveContext, cancelResolve := context.WithCancel(t.Context())
	cancelResolve()
	resolveCancelled := fixedHostDialContext(
		testNotificationHost,
		notificationResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, context.Canceled
		}),
		func(context.Context, string, string) (net.Conn, error) { return nil, errTestTransport },
	)
	if connection, err := resolveCancelled(
		resolveContext, testTCPNetwork, testNotificationHost+":443",
	); connection != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled resolution = %v, %v", connection, err)
	}

	dialContext, cancelDial := context.WithCancel(t.Context())
	dialCancelled := fixedHostDialContext(
		testNotificationHost,
		notificationResolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
			return notificationAddresses("1.1.1.1"), nil
		}),
		func(context.Context, string, string) (net.Conn, error) {
			cancelDial()

			return nil, errTestTransport
		},
	)
	if connection, err := dialCancelled(
		dialContext, testTCPNetwork, testNotificationHost+":443",
	); connection != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled connection = %v, %v", connection, err)
	}
}

type notificationTransportPolicy struct {
	clientTimeout       time.Duration
	redirect            bool
	direct              bool
	dial                bool
	noTLSDial           bool
	minimumTLS          uint16
	serverName          string
	compressionDisabled bool
	maximumIdle         int
	maximumIdlePerHost  int
	maximumConnections  int
	idleTimeout         time.Duration
	responseTimeout     time.Duration
	maximumHeaders      int64
	http1               bool
	http2               bool
	noUnencryptedHTTP2  bool
}

func observedNotificationTransportPolicy(
	client *http.Client,
	transport *http.Transport,
) notificationTransportPolicy {
	policy := notificationTransportPolicy{
		clientTimeout: client.Timeout, redirect: client.CheckRedirect != nil,
		direct: transport.Proxy == nil, dial: transport.DialContext != nil,
		noTLSDial:           transport.DialTLSContext == nil,
		compressionDisabled: transport.DisableCompression,
		maximumIdle:         transport.MaxIdleConns, maximumIdlePerHost: transport.MaxIdleConnsPerHost,
		maximumConnections: transport.MaxConnsPerHost, idleTimeout: transport.IdleConnTimeout,
		responseTimeout: transport.ResponseHeaderTimeout, maximumHeaders: transport.MaxResponseHeaderBytes,
	}
	if transport.TLSClientConfig != nil {
		policy.minimumTLS = transport.TLSClientConfig.MinVersion
		policy.serverName = transport.TLSClientConfig.ServerName
	}
	if transport.Protocols != nil {
		policy.http1 = transport.Protocols.HTTP1()
		policy.http2 = transport.Protocols.HTTP2()
		policy.noUnencryptedHTTP2 = !transport.Protocols.UnencryptedHTTP2()
	}

	return policy
}

func notificationAddresses(values ...string) []netip.Addr {
	addresses := make([]netip.Addr, len(values))
	for index, value := range values {
		addresses[index] = netip.MustParseAddr(value)
	}

	return addresses
}

func testPipe(t *testing.T) net.Conn {
	t.Helper()

	client, server := net.Pipe()
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})

	return client
}

func stringsContain(err error, value string) bool {
	return err != nil && strings.Contains(err.Error(), value)
}
