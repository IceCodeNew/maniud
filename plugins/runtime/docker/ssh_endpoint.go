package docker

import (
	"net"
	"net/url"
	"os/user"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	defaultSSHPort       = "22"
	maximumSSHFiles      = 8
	maximumPrivateKey    = 1 << 20
	maximumPassphrase    = 4 << 10
	maximumSSHValueBytes = 4 << 10
	dockerDialCommand    = "docker system dial-stdio"
)

// SSHKeyPassphrase returns a disposable passphrase for an encrypted private
// key. SSHEndpoint erases the returned byte slice before it returns.
type SSHKeyPassphrase func(privateKeyPath string) ([]byte, error)

// SSHAuth configures explicit client authentication for a Docker SSH endpoint.
// At least one private key or an agent socket is required.
type SSHAuth struct {
	AgentSocket     string
	PrivateKeyFiles []string
	Passphrase      SSHKeyPassphrase
}

// SSHHostKeys configures read-only OpenSSH known_hosts files. When Files is
// empty, SSHEndpoint reads existing user and system default files.
type SSHHostKeys struct {
	Files []string
}

// SSHOptions configures the direct SSH transport used by Docker dial-stdio.
type SSHOptions struct {
	Auth             SSHAuth
	HostKeys         SSHHostKeys
	RemoteDockerPath string
}

// SSHEndpoint creates an HTTP-over-SSH endpoint. It opens a direct SSH
// connection and runs Docker's fixed system dial-stdio protocol remotely.
func SSHEndpoint(address string, options SSHOptions) (Endpoint, error) {
	username, authority, err := parseSSHAddress(address)
	if err != nil {
		return Endpoint{}, err
	}

	hostKeyCallback, err := loadHostKeyCallback(options.HostKeys.Files)
	if err != nil {
		return Endpoint{}, ErrInvalidEndpoint
	}

	auth, err := loadSSHAuth(options.Auth)
	if err != nil {
		return Endpoint{}, err
	}

	command, err := remoteDockerCommand(options.RemoteDockerPath)
	if err != nil {
		return Endpoint{}, err
	}

	dialer := sshDialer{
		username:        username,
		authority:       authority,
		hostKeyCallback: hostKeyCallback,
		privateSigners:  auth.privateSigners,
		agentSocket:     auth.agentSocket,
		command:         command,
	}
	transport := baseTransport()
	transport.DisableCompression = true
	transport.DialContext = dialer.DialContext
	baseURL := url.URL{ //nolint:exhaustruct // A request base intentionally has no path, query, user, or fragment.
		Scheme: httpScheme,
		Host:   dummyDockerHost,
	}

	return Endpoint{baseURL: baseURL, transport: transport}, nil
}

func parseSSHAddress(address string) (string, string, error) {
	parsed, err := url.Parse(address)
	if err != nil || !validSSHURL(parsed) {
		return "", "", ErrInvalidEndpoint
	}

	username, err := sshUsername(parsed, user.Current)
	if err != nil {
		return "", "", err
	}

	authority, err := sshAuthority(parsed)
	if err != nil {
		return "", "", err
	}

	return username, authority, nil
}

func validSSHURL(parsed *url.URL) bool {
	if parsed.Scheme != "ssh" || parsed.Opaque != "" || !emptyNetworkResource(parsed) ||
		parsed.Hostname() == "" || !validSSHValue(parsed.Hostname()) {
		return false
	}

	if parsed.User == nil {
		return true
	}

	_, password := parsed.User.Password()

	return !password
}

func sshUsername(parsed *url.URL, currentUser func() (*user.User, error)) (string, error) {
	username := ""
	if parsed.User != nil {
		username = parsed.User.Username()
	}

	if username == "" {
		current, currentErr := currentUser()
		if currentErr != nil {
			return "", ErrInvalidEndpoint
		}

		username = current.Username
	}

	if !validSSHValue(username) {
		return "", ErrInvalidEndpoint
	}

	return username, nil
}

func sshAuthority(parsed *url.URL) (string, error) {
	port := parsed.Port()
	if port == "" {
		port = defaultSSHPort
	}

	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return "", ErrInvalidEndpoint
	}

	return net.JoinHostPort(parsed.Hostname(), port), nil
}

func remoteDockerCommand(executable string) (string, error) {
	if executable == "" {
		return dockerDialCommand, nil
	}

	if !path.IsAbs(executable) || path.Clean(executable) != executable ||
		len(executable) > maximumSSHValueBytes {
		return "", ErrInvalidEndpoint
	}

	for _, character := range executable {
		if !remotePathCharacter(character) {
			return "", ErrInvalidEndpoint
		}
	}

	return executable + " system dial-stdio", nil
}

func remotePathCharacter(character rune) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
		character >= '0' && character <= '9' || strings.ContainsRune("/._+-", character)
}

func validSSHValue(value string) bool {
	if value == "" || len(value) > maximumSSHValueBytes || !utf8.ValidString(value) {
		return false
	}

	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}

	return true
}

func validAbsolutePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value &&
		!strings.ContainsRune(value, '\x00') && len(value) <= maximumSSHValueBytes
}
