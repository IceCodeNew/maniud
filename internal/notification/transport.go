// Package notification delivers bounded application events to fixed external
// notification targets without changing the owning operation's result.
package notification

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"
)

const (
	notificationHTTPSPort          = "443"
	notificationRequestTimeout     = 5 * time.Second
	notificationKeepAlive          = 30 * time.Second
	notificationIdleTimeout        = 30 * time.Second
	notificationMaximumConnections = 2
	notificationMaximumHeaderBytes = 16 << 10
)

var (
	errTransportPolicy      = errors.New("notification transport policy rejected request")
	errTransportUnavailable = errors.New("notification transport is unavailable")
)

type notificationResolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

type notificationDialContext func(
	ctx context.Context,
	network string,
	address string,
) (net.Conn, error)

func newNotificationHTTPClient(host string) *http.Client {
	dialer := &net.Dialer{Timeout: notificationRequestTimeout, KeepAlive: notificationKeepAlive}

	return newNotificationHTTPClientWith(host, net.DefaultResolver, dialer.DialContext)
}

func newNotificationHTTPClientWith(
	host string,
	resolver notificationResolver,
	dial notificationDialContext,
) *http.Client {
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            fixedHostDialContext(host, resolver, dial),
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host},
		TLSHandshakeTimeout:    notificationRequestTimeout,
		DisableCompression:     true,
		MaxIdleConns:           notificationMaximumConnections,
		MaxIdleConnsPerHost:    1,
		MaxConnsPerHost:        notificationMaximumConnections,
		IdleConnTimeout:        notificationIdleTimeout,
		ResponseHeaderTimeout:  notificationRequestTimeout,
		MaxResponseHeaderBytes: notificationMaximumHeaderBytes,
		Protocols:              protocols,
	}

	return &http.Client{
		Transport: transport,
		Timeout:   notificationRequestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errTransportPolicy
		},
	}
}

func fixedHostDialContext(
	host string,
	resolver notificationResolver,
	dial notificationDialContext,
) notificationDialContext {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		lookupNetwork, err := notificationDialTarget(network, address, host)
		if err != nil {
			return nil, err
		}
		addresses, err := trustedNotificationAddresses(ctx, lookupNetwork, host, resolver)
		if err != nil {
			return nil, err
		}

		return dialNotificationAddresses(ctx, network, addresses, dial)
	}
}

func notificationDialTarget(network, address, host string) (string, error) {
	lookupNetwork, valid := notificationLookupNetwork(network)
	requestedHost, port, err := net.SplitHostPort(address)
	if !valid || err != nil || !strings.EqualFold(requestedHost, host) || port != notificationHTTPSPort {
		return "", errTransportPolicy
	}

	return lookupNetwork, nil
}

func trustedNotificationAddresses(
	ctx context.Context,
	network string,
	host string,
	resolver notificationResolver,
) ([]netip.Addr, error) {
	addresses, err := resolver.LookupNetIP(ctx, network, host)
	if err != nil {
		return nil, notificationTransportError(ctx)
	}
	if len(addresses) == 0 {
		return nil, errTransportPolicy
	}
	for _, address := range addresses {
		if !publicNotificationAddress(address) {
			return nil, errTransportPolicy
		}
	}

	return addresses, nil
}

func dialNotificationAddresses(
	ctx context.Context,
	network string,
	addresses []netip.Addr,
	dial notificationDialContext,
) (net.Conn, error) {
	for _, address := range addresses {
		connection, err := dial(
			ctx,
			network,
			net.JoinHostPort(address.Unmap().String(), notificationHTTPSPort),
		)
		if err == nil {
			return connection, nil
		}
		if ctx.Err() != nil {
			return nil, notificationTransportError(ctx)
		}
	}

	return nil, errTransportUnavailable
}

func notificationLookupNetwork(network string) (string, bool) {
	switch network {
	case "tcp":
		return "ip", true
	case "tcp4":
		return "ip4", true
	case "tcp6":
		return "ip6", true
	default:
		return "", false
	}
}

func notificationTransportError(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("notification transport cancelled: %w", err)
	}

	return errTransportUnavailable
}

func publicNotificationAddress(address netip.Addr) bool {
	if !address.IsValid() || address.Zone() != "" {
		return false
	}

	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() {
		return false
	}
	if address.Is4() {
		return !specialPurposeIPv4(address.As4())
	}

	bytes := address.As16()

	return bytes[0]&0xe0 == 0x20 && !documentationIPv6(bytes)
}

func specialPurposeIPv4(address [4]byte) bool {
	ranges := [...]netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("192.0.2.0/24"),
		netip.MustParsePrefix("192.88.99.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("198.51.100.0/24"),
		netip.MustParsePrefix("203.0.113.0/24"),
		netip.MustParsePrefix("240.0.0.0/4"),
	}
	value := netip.AddrFrom4(address)
	for _, candidate := range ranges {
		if candidate.Contains(value) {
			return true
		}
	}

	return false
}

func documentationIPv6(address [16]byte) bool {
	return address[0] == 0x20 && address[1] == 0x01 && address[2] == 0x0d && address[3] == 0xb8
}
