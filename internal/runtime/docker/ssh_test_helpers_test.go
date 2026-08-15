package docker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

var errUnexpectedSSHTestClientKey = errors.New("unexpected test client key")

type sshTestIdentity struct {
	privateKey ed25519.PrivateKey
	signer     ssh.Signer
}

func newSSHTestIdentity(t *testing.T) sshTestIdentity {
	t.Helper()

	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey() error = %v", err)
	}

	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey() error = %v", err)
	}

	return sshTestIdentity{privateKey: privateKey, signer: signer}
}

func writeSSHPrivateKey(t *testing.T, identity sshTestIdentity, passphrase []byte) string {
	t.Helper()

	var (
		block *pem.Block
		err   error
	)
	if passphrase == nil {
		block, err = ssh.MarshalPrivateKey(identity.privateKey, "maniud-test")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(identity.privateKey, "maniud-test", passphrase)
	}

	if err != nil {
		t.Fatalf("marshal private key error = %v", err)
	}

	filename := filepath.Join(t.TempDir(), "id_ed25519")

	err = os.WriteFile(filename, pem.EncodeToMemory(block), 0o600)
	if err != nil {
		t.Fatalf("os.WriteFile(private key) error = %v", err)
	}

	return filename
}

type sshServerMode uint8

const (
	sshServerNormal sshServerMode = iota
	sshServerRejectChannel
	sshServerIgnoreChannel
	sshServerRejectExec
	sshServerIgnoreExec
)

type sshTestServer struct {
	listener net.Listener
	config   *ssh.ServerConfig
	handler  http.Handler
	mode     sshServerMode
	stderr   []byte

	commands chan string
	requests chan string

	mu          sync.Mutex
	connections map[net.Conn]struct{}
}

func newSSHTestListener(t *testing.T, network, address string) net.Listener {
	t.Helper()

	listenConfig := net.ListenConfig{
		Control:         nil,
		KeepAlive:       0,
		KeepAliveConfig: net.KeepAliveConfig{Enable: false, Idle: 0, Interval: 0, Count: 0},
	}

	listener, err := listenConfig.Listen(context.Background(), network, address)
	if err != nil {
		t.Fatalf("net.Listen(%s) error = %v", network, err)
	}

	t.Cleanup(func() {
		_ = listener.Close()
	})

	return listener
}

func newSSHTestServer(
	t *testing.T,
	serverIdentity sshTestIdentity,
	clientIdentity sshTestIdentity,
	handler http.Handler,
	mode sshServerMode,
	stderr []byte,
) *sshTestServer {
	t.Helper()

	listener := newSSHTestListener(t, "tcp", "127.0.0.1:0")

	config := &ssh.ServerConfig{ //nolint:exhaustruct // Tests exercise public-key authentication only.
		PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if !bytes.Equal(key.Marshal(), clientIdentity.signer.PublicKey().Marshal()) {
				return nil, errUnexpectedSSHTestClientKey
			}

			return &ssh.Permissions{}, nil //nolint:exhaustruct // The test grants no certificate permissions.
		},
	}
	config.AddHostKey(serverIdentity.signer)

	server := &sshTestServer{
		listener:    listener,
		config:      config,
		handler:     handler,
		mode:        mode,
		stderr:      stderr,
		commands:    make(chan string, 8),
		requests:    make(chan string, 8),
		mu:          sync.Mutex{},
		connections: make(map[net.Conn]struct{}),
	}
	go server.serve()

	t.Cleanup(server.close)

	return server
}

func (server *sshTestServer) address() string {
	return server.listener.Addr().String()
}

func (server *sshTestServer) endpointURL() string {
	return "ssh://operator@" + server.address()
}

func (server *sshTestServer) writeKnownHosts(t *testing.T, key ssh.PublicKey, revoked bool) string {
	t.Helper()

	line := knownhosts.Line([]string{server.address()}, key)
	if revoked {
		line = "@revoked " + line
	}

	filename := filepath.Join(t.TempDir(), "known_hosts")

	err := os.WriteFile(filename, []byte(line+"\n"), 0o600)
	if err != nil {
		t.Fatalf("os.WriteFile(known_hosts) error = %v", err)
	}

	return filename
}

func (server *sshTestServer) serve() {
	for {
		connection, err := server.listener.Accept()
		if err != nil {
			return
		}

		server.track(connection, true)
		go server.serveConnection(connection)
	}
}

func (server *sshTestServer) serveConnection(connection net.Conn) {
	defer func() {
		server.track(connection, false)
		_ = connection.Close()
	}()

	_, channels, requests, err := ssh.NewServerConn(connection, server.config)
	if err != nil {
		return
	}

	go ssh.DiscardRequests(requests)

	for channel := range channels {
		if server.mode == sshServerRejectChannel {
			_ = channel.Reject(ssh.Prohibited, "rejected")

			continue
		}

		if server.mode == sshServerIgnoreChannel {
			continue
		}

		accepted, channelRequests, err := channel.Accept()
		if err != nil {
			continue
		}

		go server.serveChannel(accepted, channelRequests)
	}
}

func (server *sshTestServer) serveChannel(channel ssh.Channel, requests <-chan *ssh.Request) {
	defer func() {
		_ = channel.Close()
	}()

	for request := range requests {
		server.requests <- request.Type

		if request.Type != sshExecRequest {
			_ = request.Reply(false, nil)

			continue
		}

		if server.mode == sshServerIgnoreExec {
			continue
		}

		if server.mode == sshServerRejectExec {
			_ = request.Reply(false, nil)

			return
		}

		var payload struct{ Command string }

		err := ssh.Unmarshal(request.Payload, &payload)
		if err != nil {
			_ = request.Reply(false, nil)

			return
		}

		server.commands <- payload.Command

		_ = request.Reply(true, nil)

		if len(server.stderr) > 0 {
			_, _ = channel.Stderr().Write(server.stderr)
		}

		server.serveHTTP(channel)

		return
	}
}

func (server *sshTestServer) serveHTTP(connection ssh.Channel) {
	reader := bufio.NewReader(connection)
	for {
		request, err := http.ReadRequest(reader)
		if err != nil {
			return
		}

		responseWriter := httptest.NewRecorder()
		server.handler.ServeHTTP(responseWriter, request)
		response := responseWriter.Result()
		response.Request = request
		response.ContentLength = int64(responseWriter.Body.Len())
		_ = response.Write(connection)

		_ = response.Body.Close()
		if request.Close {
			return
		}
	}
}

func (server *sshTestServer) track(connection net.Conn, add bool) {
	server.mu.Lock()
	defer server.mu.Unlock()

	if add {
		server.connections[connection] = struct{}{}
	} else {
		delete(server.connections, connection)
	}
}

func (server *sshTestServer) close() {
	_ = server.listener.Close()
	server.mu.Lock()

	connections := make([]net.Conn, 0, len(server.connections))
	for connection := range server.connections {
		connections = append(connections, connection)
	}
	server.mu.Unlock()

	for _, connection := range connections {
		_ = connection.Close()
	}
}

func startSSHAgent(t *testing.T, identity sshTestIdentity) string {
	t.Helper()

	listener := newSSHTestListener(t, "unix", filepath.Join(t.TempDir(), "agent.sock"))

	keyring := agent.NewKeyring()
	addedKey := agent.AddedKey{PrivateKey: identity.privateKey} //nolint:exhaustruct // Optional fields are not used.

	err := keyring.Add(addedKey)
	if err != nil {
		t.Fatalf("agent.Add() error = %v", err)
	}

	connections := make(chan net.Conn, 4)

	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}

			connections <- connection
			go func() {
				_ = agent.ServeAgent(keyring, connection)
				_ = connection.Close()
			}()
		}
	}()

	t.Cleanup(func() {
		_ = listener.Close()

		close(connections)

		for connection := range connections {
			_ = connection.Close()
		}
	})

	return listener.Addr().String()
}
