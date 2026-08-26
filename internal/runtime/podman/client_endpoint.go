package podman

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	podmanDialTimeout   = 10 * time.Second
	podmanHeaderTimeout = 15 * time.Second
	podmanIdleTimeout   = 30 * time.Second
	podmanKeepAlive     = 30 * time.Second
	maximumIdleConns    = 6
	maximumHeaderBytes  = 64 << 10
)

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
	metadata, err := os.Lstat(path) //nolint:gosec // The clean absolute path is required; Lstat rejects symlink endpoints.
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

func effectiveUserID() uint32 {
	return uint32(os.Geteuid()) //nolint:gosec // Unix effective user IDs use the uint32 uid_t domain.
}
