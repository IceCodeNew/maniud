package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	maximumSSHStderr  = 16 << 10
	sshStderrReadSize = 4 << 10
	sshAuthCapacity   = 2
	sshExecRequest    = "exec"
)

var errSSHCommandRejected = errors.New("SSH dial-stdio command rejected")

type sshDialer struct {
	username        string
	authority       string
	hostKeyCallback ssh.HostKeyCallback
	privateSigners  []ssh.Signer
	agentSocket     string
	command         string
}

func (dialer sshDialer) DialContext(ctx context.Context, _, _ string) (net.Conn, error) {
	raw, err := (&net.Dialer{ //nolint:exhaustruct // Zero values preserve the standard TCP policy for optional fields.
		Timeout:   dialTimeout,
		KeepAlive: keepAlive,
	}).DialContext(ctx, "tcp", dialer.authority)
	if err != nil {
		return nil, fmt.Errorf("dial Docker SSH endpoint: %w", err)
	}

	authMethods, agentConnection, err := dialer.authMethods(ctx)
	if err != nil {
		_ = raw.Close()

		return nil, err
	}

	if agentConnection != nil {
		defer func() {
			_ = agentConnection.Close()
		}()
	}

	config := &ssh.ClientConfig{ //nolint:exhaustruct // Algorithms and banners use x/crypto's maintained secure defaults.
		User:            dialer.username,
		Auth:            authMethods,
		HostKeyCallback: dialer.hostKeyCallback,
		Timeout:         dialTimeout,
	}

	sshConnection, channels, requests, err := newSSHClientConnection(ctx, raw, dialer.authority, config)
	if err != nil {
		return nil, err
	}

	client := ssh.NewClient(sshConnection, channels, requests)

	connection, err := openDockerSSHChannel(ctx, client, raw.LocalAddr(), raw.RemoteAddr(), dialer.command)
	if err != nil {
		_ = client.Close()

		return nil, err
	}

	return connection, nil
}

func (dialer sshDialer) authMethods(ctx context.Context) ([]ssh.AuthMethod, net.Conn, error) {
	methods := make([]ssh.AuthMethod, 0, sshAuthCapacity)
	if len(dialer.privateSigners) > 0 {
		methods = append(methods, ssh.PublicKeys(dialer.privateSigners...))
	}

	if dialer.agentSocket == "" {
		return methods, nil, nil
	}

	connection, err := (&net.Dialer{ //nolint:exhaustruct // A local Unix socket does not need TCP keepalive fields.
		Timeout: dialTimeout,
	}).DialContext(ctx, "unix", dialer.agentSocket)
	if err != nil {
		return nil, nil, fmt.Errorf("dial SSH agent: %w", err)
	}

	agentClient := agent.NewClient(connection)
	methods = append(methods, ssh.PublicKeysCallback(agentClient.Signers))

	return methods, connection, nil
}

type sshConnectionResult struct {
	connection ssh.Conn
	channels   <-chan ssh.NewChannel
	requests   <-chan *ssh.Request
	err        error
}

func newSSHClientConnection( //nolint:ireturn // x/crypto requires its Conn interface to construct an SSH client.
	ctx context.Context,
	raw net.Conn,
	authority string,
	config *ssh.ClientConfig,
) (ssh.Conn, <-chan ssh.NewChannel, <-chan *ssh.Request, error) {
	select {
	case <-ctx.Done():
		_ = raw.Close()

		return nil, nil, nil, fmt.Errorf("establish Docker SSH connection: %w", ctx.Err())
	default:
	}

	result := make(chan sshConnectionResult, 1)

	go func() {
		connection, channels, requests, err := ssh.NewClientConn(raw, authority, config)
		result <- sshConnectionResult{
			connection: connection,
			channels:   channels,
			requests:   requests,
			err:        err,
		}
	}()

	select {
	case <-ctx.Done():
		_ = raw.Close()

		return nil, nil, nil, fmt.Errorf("establish Docker SSH connection: %w", ctx.Err())
	case outcome := <-result:
		if outcome.err != nil {
			return nil, nil, nil, outcome.err
		}

		err := ctx.Err()
		if err != nil {
			_ = outcome.connection.Close()

			return nil, nil, nil, fmt.Errorf("establish Docker SSH connection: %w", err)
		}

		return outcome.connection, outcome.channels, outcome.requests, nil
	}
}

type sshChannelResult struct {
	channel  ssh.Channel
	requests <-chan *ssh.Request
	err      error
}

func openDockerSSHChannel(
	ctx context.Context,
	client *ssh.Client,
	localAddress net.Addr,
	remoteAddress net.Addr,
	command string,
) (*sshChannelConnection, error) {
	outcome, err := requestDockerSSHChannel(ctx, client)
	if err != nil {
		return nil, err
	}

	err = startDockerSSHCommand(ctx, outcome.channel, command)
	if err != nil {
		_ = outcome.channel.Close()

		return nil, err
	}

	connection := &sshChannelConnection{
		client:        client,
		channel:       outcome.channel,
		localAddress:  localAddress,
		remoteAddress: remoteAddress,
		done:          make(chan struct{}),
		closeOnce:     sync.Once{},
		mu:            sync.Mutex{},
		closed:        false,
		timer:         nil,
	}
	go ssh.DiscardRequests(outcome.requests)
	go connection.watchStderr(outcome.channel.Stderr())

	return connection, nil
}

func requestDockerSSHChannel(ctx context.Context, client *ssh.Client) (sshChannelResult, error) {
	result := make(chan sshChannelResult, 1)

	go func() {
		channel, requests, err := client.OpenChannel("session", nil)
		result <- sshChannelResult{channel: channel, requests: requests, err: err}
	}()

	var outcome sshChannelResult
	select {
	case <-ctx.Done():
		return sshChannelResult{}, fmt.Errorf("open Docker SSH channel: %w", ctx.Err())
	case outcome = <-result:
		if outcome.err != nil {
			return sshChannelResult{}, outcome.err
		}
	}

	return outcome, nil
}

func startDockerSSHCommand(ctx context.Context, channel ssh.Channel, command string) error {
	started := make(chan error, 1)

	go func() {
		accepted, err := channel.SendRequest(
			sshExecRequest,
			true,
			ssh.Marshal(struct{ Command string }{Command: command}),
		)
		if err == nil && !accepted {
			err = errSSHCommandRejected
		}

		started <- err
	}()

	select {
	case <-ctx.Done():
		return fmt.Errorf("start Docker SSH command: %w", ctx.Err())
	case err := <-started:
		if err != nil {
			return err
		}
	}

	return nil
}

type sshChannelConnection struct {
	client        *ssh.Client
	channel       ssh.Channel
	localAddress  net.Addr
	remoteAddress net.Addr
	done          chan struct{}

	closeOnce sync.Once
	mu        sync.Mutex
	closed    bool
	timer     *time.Timer
}

func (connection *sshChannelConnection) Read(buffer []byte) (int, error) {
	// net.Conn consumers require exact io.EOF and timeout identities.
	return connection.channel.Read(buffer) //nolint:wrapcheck // Preserve the net.Conn error contract.
}

func (connection *sshChannelConnection) Write(buffer []byte) (int, error) {
	return connection.channel.Write(buffer) //nolint:wrapcheck // Preserve the net.Conn error contract.
}

func (connection *sshChannelConnection) Close() error {
	var closeErr error

	connection.closeOnce.Do(func() {
		connection.mu.Lock()

		connection.closed = true
		if connection.timer != nil {
			connection.timer.Stop()
		}

		close(connection.done)
		connection.mu.Unlock()

		closeErr = errors.Join(connection.channel.Close(), connection.client.Close())
	})

	return closeErr
}

func (connection *sshChannelConnection) LocalAddr() net.Addr {
	return connection.localAddress
}

func (connection *sshChannelConnection) RemoteAddr() net.Addr {
	return connection.remoteAddress
}

func (connection *sshChannelConnection) SetDeadline(deadline time.Time) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()

	if connection.closed {
		return net.ErrClosed
	}

	if connection.timer != nil {
		connection.timer.Stop()
		connection.timer = nil
	}

	if deadline.IsZero() {
		return nil
	}

	delay := max(time.Until(deadline), 0)

	connection.timer = time.AfterFunc(delay, func() {
		_ = connection.Close()
	})

	return nil
}

func (connection *sshChannelConnection) SetReadDeadline(deadline time.Time) error {
	return connection.SetDeadline(deadline)
}

func (connection *sshChannelConnection) SetWriteDeadline(deadline time.Time) error {
	return connection.SetDeadline(deadline)
}

func (connection *sshChannelConnection) watchStderr(stderr io.Reader) {
	buffer := make([]byte, sshStderrReadSize)
	total := 0

	for {
		read, err := stderr.Read(buffer)

		total += read
		if total > maximumSSHStderr {
			_ = connection.Close()

			return
		}

		if err != nil {
			return
		}

		select {
		case <-connection.done:
			return
		default:
		}
	}
}
