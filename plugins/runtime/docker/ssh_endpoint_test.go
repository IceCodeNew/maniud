package docker

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func privateKeySSHOptions(privateKey, knownHosts string) SSHOptions {
	return SSHOptions{
		Auth: SSHAuth{
			AgentSocket:     "",
			PrivateKeyFiles: []string{privateKey},
			Passphrase:      nil,
		},
		HostKeys:         SSHHostKeys{Files: []string{knownHosts}},
		RemoteDockerPath: "",
	}
}

func assertSSHCommand(t *testing.T, server *sshTestServer, want string) {
	t.Helper()

	select {
	case command := <-server.commands:
		if command != want {
			t.Fatalf("SSH command = %q, want %q", command, want)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH server did not receive exec command")
	}
}

func assertOnlySSHExecRequests(t *testing.T, server *sshTestServer) {
	t.Helper()

	for {
		select {
		case request := <-server.requests:
			if request != sshExecRequest {
				t.Fatalf("unexpected SSH request = %q", request)
			}
		default:
			return
		}
	}
}

func writeKnownHostsFile(t *testing.T, contents string) string {
	t.Helper()

	filename := filepath.Join(t.TempDir(), "known_hosts")

	err := os.WriteFile(filename, []byte(contents), 0o600)
	if err != nil {
		t.Fatalf("os.WriteFile(known_hosts) error = %v", err)
	}

	return filename
}

func writePrivateKeyFixture(t *testing.T, name string, contents []byte) string {
	t.Helper()

	filename := filepath.Join(t.TempDir(), name)

	err := os.WriteFile(filename, contents, 0o600)
	if err != nil {
		t.Fatalf("os.WriteFile(%s) error = %v", name, err)
	}

	return filename
}

func assertInvalidSSHAddresses(t *testing.T, options SSHOptions) {
	t.Helper()

	addresses := []string{
		"http://operator@" + testEngineHostname + ":22",
		"ssh:operator@" + testEngineHostname,
		"ssh://operator:secret@" + testEngineHostname + ":22",
		"ssh://operator@" + testEngineHostname + ":22/path",
		"ssh://operator@" + testEngineHostname + ":22?query=1",
		"ssh://operator@" + testEngineHostname + ":22#fragment",
		"ssh://operator@:22",
		"ssh://operator@" + testEngineHostname + ":0",
		"ssh://operator@" + testEngineHostname + ":65536",
		"ssh://operator%20name@" + testEngineHostname + ":22",
	}
	for _, address := range addresses {
		_, err := SSHEndpoint(address, options)
		if !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("SSHEndpoint(%q) error = %v, want ErrInvalidEndpoint", address, err)
		}
	}
}

type canceledAfterWorkContext struct{}

func (canceledAfterWorkContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (canceledAfterWorkContext) Done() <-chan struct{} {
	return nil
}

func (canceledAfterWorkContext) Err() error {
	return context.Canceled
}

func (canceledAfterWorkContext) Value(any) any {
	return nil
}

type notifyingWriteConnection struct {
	net.Conn

	started chan<- struct{}
}

func (connection notifyingWriteConnection) Write(content []byte) (int, error) {
	select {
	case connection.started <- struct{}{}:
	default:
	}

	return connection.Conn.Write(content) //nolint:wrapcheck // The fixture preserves the net.Conn contract.
}

func TestSSHEndpointNegotiatesDockerEngine(t *testing.T) {
	t.Parallel()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	server := newSSHTestServer(
		t,
		serverIdentity,
		clientIdentity,
		engineHandler(t, validEngineFixture(maximumAPIVersion)),
		sshServerNormal,
		[]byte("bounded remote diagnostic"),
	)
	privateKey := writeSSHPrivateKey(t, clientIdentity, nil)
	knownHosts := server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), false)

	endpoint, err := SSHEndpoint(server.endpointURL(), privateKeySSHOptions(privateKey, knownHosts))
	if err != nil {
		t.Fatalf("SSHEndpoint() error = %v", err)
	}

	client, version, err := Connect(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Connect(SSH) error = %v", err)
	}

	client.CloseIdleConnections()

	if version.Protocol != maximumAPIVersion {
		t.Fatalf("Connect(SSH) protocol = %q", version.Protocol)
	}

	assertSSHCommand(t, server, dockerDialCommand)
	assertOnlySSHExecRequests(t, server)
}

func TestSSHEndpointUsesEncryptedKeyAndAbsoluteDockerPath(t *testing.T) {
	t.Parallel()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	server := newSSHTestServer(
		t,
		serverIdentity,
		clientIdentity,
		engineHandler(t, validEngineFixture(minimumAPIVersion)),
		sshServerNormal,
		nil,
	)

	const passphrase = "correct horse battery staple"

	privateKey := writeSSHPrivateKey(t, clientIdentity, []byte(passphrase))
	knownHosts := server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), false)
	secret := []byte(passphrase)

	endpoint, err := SSHEndpoint(server.endpointURL(), SSHOptions{
		Auth: SSHAuth{
			AgentSocket:     "",
			PrivateKeyFiles: []string{privateKey},
			Passphrase: func(filename string) ([]byte, error) {
				if filename != privateKey {
					t.Fatalf("passphrase filename = %q", filename)
				}

				return secret, nil
			},
		},
		HostKeys: SSHHostKeys{
			Files: []string{knownHosts},
		},
		RemoteDockerPath: "/opt/docker/bin/docker",
	})
	if err != nil {
		t.Fatalf("SSHEndpoint(encrypted) error = %v", err)
	}

	if strings.Trim(string(secret), "\x00") != "" {
		t.Fatal("SSHEndpoint() did not erase passphrase bytes")
	}

	client, _, err := Connect(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Connect(encrypted SSH) error = %v", err)
	}

	client.CloseIdleConnections()

	select {
	case command := <-server.commands:
		if command != "/opt/docker/bin/docker system dial-stdio" {
			t.Fatalf("SSH absolute command = %q", command)
		}
	case <-time.After(time.Second):
		t.Fatal("SSH server did not receive absolute command")
	}
}

func TestSSHEndpointUsesExplicitAgent(t *testing.T) {
	t.Parallel()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	server := newSSHTestServer(
		t,
		serverIdentity,
		clientIdentity,
		engineHandler(t, validEngineFixture(maximumAPIVersion)),
		sshServerNormal,
		nil,
	)

	endpoint, err := SSHEndpoint(server.endpointURL(), SSHOptions{
		Auth: SSHAuth{
			AgentSocket:     startSSHAgent(t, clientIdentity),
			PrivateKeyFiles: nil,
			Passphrase:      nil,
		},
		HostKeys: SSHHostKeys{
			Files: []string{server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), false)},
		},
		RemoteDockerPath: "",
	})
	if err != nil {
		t.Fatalf("SSHEndpoint(agent) error = %v", err)
	}

	client, _, err := Connect(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Connect(agent SSH) error = %v", err)
	}

	client.CloseIdleConnections()
}

func TestSSHEndpointReadsDefaultKnownHosts(t *testing.T) {
	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	server := newSSHTestServer(
		t,
		serverIdentity,
		clientIdentity,
		engineHandler(t, validEngineFixture(maximumAPIVersion)),
		sshServerNormal,
		nil,
	)
	home := t.TempDir()

	knownHostsDirectory := filepath.Join(home, ".ssh")

	err := os.Mkdir(knownHostsDirectory, 0o700)
	if err != nil {
		t.Fatalf("os.Mkdir(.ssh) error = %v", err)
	}

	line := knownhosts.Line([]string{server.address()}, serverIdentity.signer.PublicKey())

	err = os.WriteFile(filepath.Join(knownHostsDirectory, "known_hosts"), []byte(line+"\n"), 0o600)
	if err != nil {
		t.Fatalf("os.WriteFile(default known_hosts) error = %v", err)
	}

	t.Setenv("HOME", home)

	endpoint, err := SSHEndpoint(server.endpointURL(), SSHOptions{
		Auth: SSHAuth{
			AgentSocket:     "",
			PrivateKeyFiles: []string{writeSSHPrivateKey(t, clientIdentity, nil)},
			Passphrase:      nil,
		},
		HostKeys:         SSHHostKeys{Files: nil},
		RemoteDockerPath: "",
	})
	if err != nil {
		t.Fatalf("SSHEndpoint(default known_hosts) error = %v", err)
	}

	client, _, err := Connect(context.Background(), endpoint)
	if err != nil {
		t.Fatalf("Connect(default known_hosts) error = %v", err)
	}

	client.CloseIdleConnections()
}

func TestSSHAddressUsesUserAndPortDefaults(t *testing.T) {
	t.Parallel()

	current, err := user.Current()
	if err != nil {
		t.Fatalf("user.Current() error = %v", err)
	}

	username, authority, err := parseSSHAddress("ssh://" + testEngineHostname)
	if err != nil {
		t.Fatalf("parseSSHAddress(defaults) error = %v", err)
	}

	if username != current.Username || authority != testEngineHostname+":"+defaultSSHPort {
		t.Fatalf("parseSSHAddress(defaults) = %q, %q", username, authority)
	}

	parsed, err := url.Parse("ssh://" + testEngineHostname)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}

	_, err = sshUsername(parsed, func() (*user.User, error) {
		return nil, io.ErrUnexpectedEOF
	})
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("sshUsername(current user failure) error = %v", err)
	}
}

func TestSSHHostKeyAndValueValidation(t *testing.T) {
	t.Parallel()

	tooMany := make([]string, maximumSSHFiles+1)

	_, err := loadHostKeyCallback(tooMany)
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("loadHostKeyCallback(too many) error = %v", err)
	}

	_, err = knownHostsFiles(tooMany, os.UserHomeDir, os.Stat)
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("knownHostsFiles(too many) error = %v", err)
	}

	_, err = knownHostsFiles(nil, func() (string, error) {
		return "", io.ErrUnexpectedEOF
	}, os.Stat)
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("knownHostsFiles(home failure) error = %v", err)
	}

	home := t.TempDir()

	err = os.MkdirAll(filepath.Join(home, ".ssh", "known_hosts"), 0o700)
	if err != nil {
		t.Fatalf("os.MkdirAll(known_hosts directory) error = %v", err)
	}

	_, err = knownHostsFiles(nil, func() (string, error) { return home, nil }, os.Stat)
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("knownHostsFiles(directory) error = %v", err)
	}

	if validSSHValue("operator\n") {
		t.Fatal("validSSHValue(control) = true")
	}

	if validSSHValue("") {
		t.Fatal("validSSHValue(empty) = true")
	}
}

func TestSSHDefaultKnownHostsIncludesRegularHomeFile(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	knownHosts := filepath.Join(home, ".ssh", "known_hosts")

	err := os.MkdirAll(filepath.Dir(knownHosts), 0o700)
	if err != nil {
		t.Fatalf("os.MkdirAll(known_hosts) error = %v", err)
	}

	err = os.WriteFile(knownHosts, nil, 0o600)
	if err != nil {
		t.Fatalf("os.WriteFile(known_hosts) error = %v", err)
	}

	files, err := knownHostsFiles(nil, func() (string, error) { return home, nil }, os.Stat)
	if err != nil || len(files) == 0 || files[0] != knownHosts {
		t.Fatalf("knownHostsFiles() = %v, %v", files, err)
	}
}

func TestSSHDefaultKnownHostsRejectsFileDiscoveryFailure(t *testing.T) {
	t.Parallel()

	_, err := knownHostsFiles(
		nil,
		func() (string, error) { return "/home/operator", nil },
		func(string) (os.FileInfo, error) { return nil, io.ErrUnexpectedEOF },
	)
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("knownHostsFiles(stat failure) error = %v", err)
	}
}

func TestSSHDefaultKnownHostsIgnoresMissingFiles(t *testing.T) {
	t.Parallel()

	files, err := knownHostsFiles(
		nil,
		func() (string, error) { return "/home/operator", nil },
		func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
	)
	if err != nil || len(files) != 0 {
		t.Fatalf("knownHostsFiles(missing files) = %v, %v", files, err)
	}
}

func TestSSHConfiguredKnownHostsValidation(t *testing.T) {
	t.Parallel()

	valid := writeKnownHostsFile(t, "host ssh-ed25519 AAAA\n")
	directory := t.TempDir()

	tests := [][]string{
		{"relative"},
		{valid, valid},
		{directory},
		{filepath.Join(t.TempDir(), "missing")},
	}
	for index, files := range tests {
		_, err := validateKnownHostsFiles(files)
		if !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("validateKnownHostsFiles(test %d) error = %v", index, err)
		}
	}
}

func TestSSHEndpointFailsClosedForHostKeyEvidence(t *testing.T) {
	t.Parallel()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	server := newSSHTestServer(
		t,
		serverIdentity,
		clientIdentity,
		engineHandler(t, validEngineFixture(maximumAPIVersion)),
		sshServerNormal,
		nil,
	)
	privateKey := writeSSHPrivateKey(t, clientIdentity, nil)
	otherIdentity := newSSHTestIdentity(t)

	tests := []struct {
		name       string
		knownHosts string
	}{
		{
			name:       "absent",
			knownHosts: filepath.Join(t.TempDir(), "known_hosts"),
		},
		{
			name:       "mismatch",
			knownHosts: server.writeKnownHosts(t, otherIdentity.signer.PublicKey(), false),
		},
		{
			name:       "revoked",
			knownHosts: server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), true),
		},
	}

	err := os.WriteFile(tests[0].knownHosts, nil, 0o600)
	if err != nil {
		t.Fatalf("os.WriteFile(empty known_hosts) error = %v", err)
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			endpoint, err := SSHEndpoint(
				server.endpointURL(),
				privateKeySSHOptions(privateKey, test.knownHosts),
			)
			if err != nil {
				t.Fatalf("SSHEndpoint() error = %v", err)
			}

			_, _, err = Connect(context.Background(), endpoint)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("Connect(%s host key) error = %v, want ErrUnavailable", test.name, err)
			}
		})
	}
}

func TestSSHEndpointValidation(t *testing.T) {
	t.Parallel()

	identity := newSSHTestIdentity(t)
	privateKey := writeSSHPrivateKey(t, identity, nil)

	knownHosts := writeKnownHostsFile(t, "malformed\n")
	line := knownhosts.Line([]string{testEngineHostname}, identity.signer.PublicKey())
	validKnownHosts := writeKnownHostsFile(t, line+"\n")

	validOptions := SSHOptions{
		Auth: SSHAuth{
			AgentSocket:     "",
			PrivateKeyFiles: []string{privateKey},
			Passphrase:      nil,
		},
		HostKeys: SSHHostKeys{
			Files: []string{validKnownHosts},
		},
		RemoteDockerPath: "",
	}

	assertInvalidSSHAddresses(t, validOptions)

	_, err := SSHEndpoint("ssh://operator@"+testEngineHostname+":22", SSHOptions{
		Auth:             validOptions.Auth,
		HostKeys:         SSHHostKeys{Files: []string{knownHosts}},
		RemoteDockerPath: "",
	})
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("SSHEndpoint(malformed known_hosts) error = %v", err)
	}

	_, err = SSHEndpoint("ssh://operator@"+testEngineHostname+":22", SSHOptions{
		Auth:             SSHAuth{AgentSocket: "", PrivateKeyFiles: nil, Passphrase: nil},
		HostKeys:         validOptions.HostKeys,
		RemoteDockerPath: "",
	})
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("SSHEndpoint(no auth) error = %v", err)
	}

	invalidCommands := []string{"relative/docker", "/usr/../bin/docker", "/usr/bin/docker command", "/usr/bin/docker;id"}
	for _, command := range invalidCommands {
		options := validOptions
		options.RemoteDockerPath = command

		_, err := SSHEndpoint("ssh://operator@"+testEngineHostname+":22", options)
		if !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("SSHEndpoint(command %q) error = %v", command, err)
		}
	}
}

func TestSSHAuthenticationValidation(t *testing.T) {
	t.Parallel()

	identity := newSSHTestIdentity(t)
	privateKey := writeSSHPrivateKey(t, identity, nil)

	tests := []SSHAuth{
		{AgentSocket: "", PrivateKeyFiles: nil, Passphrase: nil},
		{AgentSocket: "relative-agent.sock", PrivateKeyFiles: nil, Passphrase: nil},
		{AgentSocket: "", PrivateKeyFiles: []string{"relative-key"}, Passphrase: nil},
		{AgentSocket: "", PrivateKeyFiles: []string{privateKey, privateKey}, Passphrase: nil},
		{AgentSocket: "", PrivateKeyFiles: make([]string, maximumSSHFiles+1), Passphrase: nil},
	}
	for index, auth := range tests {
		_, err := loadSSHAuth(auth)
		if !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("loadSSHAuth(test %d) error = %v", index, err)
		}
	}
}

func TestSSHPrivateKeyFileValidation(t *testing.T) {
	t.Parallel()

	identity := newSSHTestIdentity(t)
	badPermission := writeSSHPrivateKey(t, identity, nil)

	err := os.Chmod(badPermission, 0o644) //nolint:gosec // The test verifies that group-readable keys fail closed.
	if err != nil {
		t.Fatalf("os.Chmod() error = %v", err)
	}

	invalidKey := writePrivateKeyFixture(t, "id_invalid", []byte("not a key"))
	emptyKey := writePrivateKeyFixture(t, "id_empty", nil)
	largeKey := writePrivateKeyFixture(t, "id_large", make([]byte, maximumPrivateKey+1))

	invalidFiles := []string{
		badPermission,
		invalidKey,
		emptyKey,
		largeKey,
		t.TempDir(),
		filepath.Join(t.TempDir(), "missing"),
	}
	for _, filename := range invalidFiles {
		_, err := loadPrivateKey(filename, nil)
		if !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("loadPrivateKey(%q) error = %v", filename, err)
		}
	}

	_, err = loadSSHAuth(SSHAuth{AgentSocket: "", PrivateKeyFiles: []string{invalidKey}, Passphrase: nil})
	if !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("loadSSHAuth(invalid key) error = %v", err)
	}
}

func TestSSHEncryptedPrivateKeyValidation(t *testing.T) {
	t.Parallel()

	identity := newSSHTestIdentity(t)
	encrypted := writeSSHPrivateKey(t, identity, []byte("secret"))

	callbacks := []SSHKeyPassphrase{
		nil,
		func(string) ([]byte, error) { return nil, io.ErrUnexpectedEOF },
		func(string) ([]byte, error) { return nil, nil },
		func(string) ([]byte, error) { return make([]byte, maximumPassphrase+1), nil },
		func(string) ([]byte, error) { return []byte("wrong"), nil },
	}
	for index, callback := range callbacks {
		_, err := loadPrivateKey(encrypted, callback)
		if !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("loadPrivateKey(callback %d) error = %v", index, err)
		}
	}
}

func TestSSHDialFailures(t *testing.T) {
	t.Parallel()

	closedListener := newSSHTestListener(t, "tcp", "127.0.0.1:0")
	closedAddress := closedListener.Addr().String()

	err := closedListener.Close()
	if err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}

	dialer := sshDialer{
		username:        "dial-test-user",
		authority:       closedAddress,
		hostKeyCallback: nil,
		privateSigners:  nil,
		agentSocket:     "",
		command:         dockerDialCommand,
	}

	_, err = dialer.DialContext(context.Background(), "tcp", "ignored")
	if err == nil {
		t.Fatal("DialContext(closed address) error = nil")
	}

	listener := newSSHTestListener(t, "tcp", "127.0.0.1:0")
	dialer.authority = listener.Addr().String()
	dialer.agentSocket = filepath.Join(t.TempDir(), "missing-agent.sock")

	_, err = dialer.DialContext(context.Background(), "tcp", "ignored")
	if err == nil {
		t.Fatal("DialContext(missing agent) error = nil")
	}
}

func TestSSHTransportFailures(t *testing.T) {
	t.Parallel()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	privateKey := writeSSHPrivateKey(t, clientIdentity, nil)

	for _, mode := range []sshServerMode{sshServerRejectChannel, sshServerRejectExec} {
		server := newSSHTestServer(t, serverIdentity, clientIdentity, http.NotFoundHandler(), mode, nil)

		knownHosts := server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), false)

		endpoint, err := SSHEndpoint(server.endpointURL(), privateKeySSHOptions(privateKey, knownHosts))
		if err != nil {
			t.Fatalf("SSHEndpoint(mode %d) error = %v", mode, err)
		}

		_, _, err = Connect(context.Background(), endpoint)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Connect(mode %d) error = %v, want ErrUnavailable", mode, err)
		}
	}
}

func TestSSHConnectionDeadlines(t *testing.T) {
	t.Parallel()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	privateKey := writeSSHPrivateKey(t, clientIdentity, nil)

	server := newSSHTestServer(t, serverIdentity, clientIdentity, http.NotFoundHandler(), sshServerNormal, nil)

	knownHosts := server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), false)

	endpoint, err := SSHEndpoint(server.endpointURL(), privateKeySSHOptions(privateKey, knownHosts))
	if err != nil {
		t.Fatalf("SSHEndpoint() error = %v", err)
	}

	connection, err := endpoint.transport.DialContext(context.Background(), "tcp", "ignored")
	if err != nil {
		t.Fatalf("SSH DialContext() error = %v", err)
	}

	if connection.LocalAddr() == nil || connection.RemoteAddr() == nil {
		t.Fatal("SSH connection addresses are nil")
	}

	err = connection.SetReadDeadline(time.Time{})
	if err != nil {
		t.Fatalf("SetReadDeadline(zero) error = %v", err)
	}

	err = connection.SetWriteDeadline(time.Now().Add(time.Minute))
	if err != nil {
		t.Fatalf("SetWriteDeadline(future) error = %v", err)
	}

	err = connection.SetDeadline(time.Now().Add(-time.Second))
	if err != nil {
		t.Fatalf("SetDeadline(past) error = %v", err)
	}

	waitForSSHConnectionClose(t, connection)

	err = connection.SetDeadline(time.Time{})
	if !errors.Is(err, net.ErrClosed) {
		t.Fatalf("SetDeadline(closed) error = %v, want net.ErrClosed", err)
	}

	_ = connection.Close()
}

func waitForSSHConnectionClose(t *testing.T, connection net.Conn) {
	t.Helper()

	sshConnection, ok := connection.(*sshChannelConnection)
	if !ok {
		t.Fatalf("SSH connection type = %T, want *sshChannelConnection", connection)
	}
	select {
	case <-sshConnection.done:
	case <-time.After(2 * time.Second):
		t.Fatal("SSH connection did not close after its deadline")
	}
}

func TestSSHHandshakeAndChannelCancellation(t *testing.T) {
	t.Parallel()

	left, right := net.Pipe()

	t.Cleanup(func() {
		_ = left.Close()
		_ = right.Close()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	connection, channels, requests, err := newSSHClientConnection(
		ctx,
		left,
		testEngineHostname+":22",
		&ssh.ClientConfig{}, //nolint:exhaustruct // Cancellation wins before config is used.
	)
	if connection != nil || channels != nil || requests != nil {
		t.Fatal("newSSHClientConnection(canceled) returned SSH resources")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("newSSHClientConnection(canceled) error = %v", err)
	}

	for _, mode := range []sshServerMode{sshServerIgnoreChannel, sshServerIgnoreExec} {
		assertSSHBlockedCancellation(t, mode)
	}
}

func TestSSHHandshakeCancellationWhileBlocked(t *testing.T) {
	t.Parallel()

	left, right := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		_ = left.Close()
		_ = right.Close()
	})
	writeStarted := make(chan struct{}, 1)
	result := make(chan sshConnectionResult, 1)
	go func() {
		connection, channels, requests, err := newSSHClientConnection(
			ctx,
			notifyingWriteConnection{Conn: left, started: writeStarted},
			testEngineHostname+":22",
			&ssh.ClientConfig{ //nolint:exhaustruct // The blocked fixture never reaches host authentication.
				User:            "blocked-test-user",
				HostKeyCallback: func(string, net.Addr, ssh.PublicKey) error { return nil },
			},
		)
		result <- sshConnectionResult{
			connection: connection, channels: channels, requests: requests, err: err,
		}
	}()

	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("SSH handshake did not reach the blocked write")
	}
	cancel()
	var outcome sshConnectionResult
	select {
	case outcome = <-result:
	case <-time.After(time.Second):
		t.Fatal("SSH handshake did not stop after cancellation")
	}
	if outcome.connection != nil || outcome.channels != nil || outcome.requests != nil {
		t.Fatal("newSSHClientConnection(blocked cancellation) returned SSH resources")
	}
	if !errors.Is(outcome.err, context.Canceled) {
		t.Fatalf("newSSHClientConnection(blocked cancellation) error = %v", outcome.err)
	}
}

func assertSSHBlockedCancellation(t *testing.T, mode sshServerMode) {
	t.Helper()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	server := newSSHTestServer(t, serverIdentity, clientIdentity, http.NotFoundHandler(), mode, nil)

	privateKey := writeSSHPrivateKey(t, clientIdentity, nil)
	knownHosts := server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), false)

	endpoint, err := SSHEndpoint(server.endpointURL(), privateKeySSHOptions(privateKey, knownHosts))
	if err != nil {
		t.Fatalf("SSHEndpoint(mode %d) error = %v", mode, err)
	}

	cancelContext, cancelDial := context.WithCancel(context.Background())
	dialResult := make(chan error, 1)
	go func() {
		_, dialErr := endpoint.transport.DialContext(cancelContext, "tcp", "ignored")
		dialResult <- dialErr
	}()

	progress := server.requests
	wantProgress := sshExecRequest
	if mode == sshServerIgnoreChannel {
		progress = server.channels
		wantProgress = "session"
	}

	select {
	case got := <-progress:
		if got != wantProgress {
			cancelDial()
			t.Fatalf("SSH server progress = %q, want %q", got, wantProgress)
		}
	case <-time.After(time.Second):
		cancelDial()
		t.Fatalf("SSH server mode %d did not reach the blocking operation", mode)
	}

	cancelDial()

	select {
	case err = <-dialResult:
	case <-time.After(time.Second):
		t.Fatalf("DialContext(mode %d) did not stop after cancellation", mode)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DialContext(mode %d) error = %v", mode, err)
	}
}

func TestSSHHandshakeNoticesCancellationAfterWork(t *testing.T) {
	t.Parallel()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	server := newSSHTestServer(
		t,
		serverIdentity,
		clientIdentity,
		http.NotFoundHandler(),
		sshServerNormal,
		nil,
	)
	dialer := &net.Dialer{} //nolint:exhaustruct // The test needs no optional dial policy.

	raw, err := dialer.DialContext(context.Background(), "tcp", server.address())
	if err != nil {
		t.Fatalf("net.Dial() error = %v", err)
	}

	knownHosts := server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), false)

	hostKeyCallback, err := knownhosts.New(knownHosts)
	if err != nil {
		t.Fatalf("knownhosts.New() error = %v", err)
	}

	config := &ssh.ClientConfig{ //nolint:exhaustruct // The test uses x/crypto's secure algorithm defaults.
		User:            "handshake-test-user",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(clientIdentity.signer)},
		HostKeyCallback: hostKeyCallback,
		Timeout:         time.Second,
	}

	connection, channels, requests, err := newSSHClientConnection(
		canceledAfterWorkContext{},
		raw,
		server.address(),
		config,
	)
	if connection != nil || channels != nil || requests != nil {
		t.Fatal("newSSHClientConnection(post-work cancellation) returned resources")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("newSSHClientConnection(post-work cancellation) error = %v", err)
	}
}

func TestSSHStderrLimitClosesChannel(t *testing.T) {
	t.Parallel()

	serverIdentity := newSSHTestIdentity(t)
	clientIdentity := newSSHTestIdentity(t)
	server := newSSHTestServer(
		t,
		serverIdentity,
		clientIdentity,
		engineHandler(t, validEngineFixture(maximumAPIVersion)),
		sshServerNormal,
		make([]byte, maximumSSHStderr+1),
	)

	privateKey := writeSSHPrivateKey(t, clientIdentity, nil)
	knownHosts := server.writeKnownHosts(t, serverIdentity.signer.PublicKey(), false)

	endpoint, err := SSHEndpoint(server.endpointURL(), privateKeySSHOptions(privateKey, knownHosts))
	if err != nil {
		t.Fatalf("SSHEndpoint() error = %v", err)
	}

	_, _, err = Connect(context.Background(), endpoint)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Connect(stderr overflow) error = %v, want ErrUnavailable", err)
	}

	connection := &sshChannelConnection{ //nolint:exhaustruct // The direct reader test only observes the done channel.
		done: make(chan struct{}),
	}
	close(connection.done)
	connection.watchStderr(strings.NewReader("diagnostic"))
	connection.watchStderr(errorReader{})
}
