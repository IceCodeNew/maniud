package docker

import (
	"errors"
	"io"
	"os"

	"golang.org/x/crypto/ssh"
)

type loadedSSHAuth struct {
	privateSigners []ssh.Signer
	agentSocket    string
}

type loadedPrivateKey struct {
	signer ssh.Signer
}

func loadSSHAuth(config SSHAuth) (loadedSSHAuth, error) {
	if len(config.PrivateKeyFiles) > maximumSSHFiles ||
		len(config.PrivateKeyFiles) == 0 && config.AgentSocket == "" {
		return loadedSSHAuth{}, ErrInvalidEndpoint
	}

	if config.AgentSocket != "" && !validAbsolutePath(config.AgentSocket) {
		return loadedSSHAuth{}, ErrInvalidEndpoint
	}

	signers := make([]ssh.Signer, 0, len(config.PrivateKeyFiles))

	seen := make(map[string]struct{}, len(config.PrivateKeyFiles))
	for _, filename := range config.PrivateKeyFiles {
		if !validAbsolutePath(filename) {
			return loadedSSHAuth{}, ErrInvalidEndpoint
		}

		if _, duplicate := seen[filename]; duplicate {
			return loadedSSHAuth{}, ErrInvalidEndpoint
		}

		privateKey, err := loadPrivateKey(filename, config.Passphrase)
		if err != nil {
			return loadedSSHAuth{}, err
		}

		seen[filename] = struct{}{}

		signers = append(signers, privateKey.signer)
	}

	return loadedSSHAuth{privateSigners: signers, agentSocket: config.AgentSocket}, nil
}

func loadPrivateKey(filename string, passphrase SSHKeyPassphrase) (loadedPrivateKey, error) {
	contents, err := readPrivateKeyFile(filename)
	if err != nil {
		return loadedPrivateKey{}, ErrInvalidEndpoint
	}
	defer clear(contents)

	return parsePrivateKey(contents, filename, passphrase)
}

func readPrivateKeyFile(filename string) ([]byte, error) {
	file, err := os.Open(filename) //nolint:gosec // The caller validates a clean absolute path before opening it.
	if err != nil {
		return nil, ErrInvalidEndpoint
	}
	defer func() {
		_ = file.Close()
	}()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 ||
		info.Size() > maximumPrivateKey {
		return nil, ErrInvalidEndpoint
	}

	contents, err := io.ReadAll(io.LimitReader(file, maximumPrivateKey+1))
	if err != nil || len(contents) == 0 || len(contents) > maximumPrivateKey {
		return nil, ErrInvalidEndpoint
	}

	return contents, nil
}

func parsePrivateKey(contents []byte, filename string, passphrase SSHKeyPassphrase) (loadedPrivateKey, error) {
	signer, err := ssh.ParsePrivateKey(contents)

	var missing *ssh.PassphraseMissingError
	if !errors.As(err, &missing) {
		if err != nil {
			return loadedPrivateKey{}, ErrInvalidEndpoint
		}

		return loadedPrivateKey{signer: signer}, nil
	}

	if passphrase == nil {
		return loadedPrivateKey{}, ErrInvalidEndpoint
	}

	secret, err := passphrase(filename)
	if err != nil {
		return loadedPrivateKey{}, ErrInvalidEndpoint
	}
	defer clear(secret)

	if len(secret) == 0 || len(secret) > maximumPassphrase {
		return loadedPrivateKey{}, ErrInvalidEndpoint
	}

	signer, err = ssh.ParsePrivateKeyWithPassphrase(contents, secret)
	if err != nil {
		return loadedPrivateKey{}, ErrInvalidEndpoint
	}

	return loadedPrivateKey{signer: signer}, nil
}
