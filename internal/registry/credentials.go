package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"oras.land/oras-go/v2/registry/remote/auth"
	orascredentials "oras.land/oras-go/v2/registry/remote/credentials"
)

const (
	maximumDockerConfigBytes = int64(1 << 20)
	maximumCredentialBytes   = 16 << 10
	dockerConfigDirectory    = ".docker"
	dockerConfigFilename     = "config.json"
	dockerRegistryName       = "docker.io"
	dockerRegistryIndex      = "index.docker.io"
	dockerRegistryHost       = "registry-1.docker.io"
)

var errDockerCredentials = errors.New("docker registry credential configuration is invalid")

// Credentials contains credentials for one registry.
type Credentials struct {
	Username     string
	Password     string
	RefreshToken string
	AccessToken  string
}

// CredentialProvider returns explicit credentials for a normalized registry
// authority. Supplying one replaces Docker configuration lookup.
type CredentialProvider func(context.Context, string) (Credentials, error)

type credentialRouting struct {
	Helpers map[string]string `json:"credHelpers"` //nolint:tagliatelle // Docker defines this field name.
	Store   *string           `json:"credsStore"`  //nolint:tagliatelle // Docker defines this field name.
}

func dockerCredentialProvider(configPath string) CredentialProvider {
	path := dockerConfigPath(configPath)
	if path == "" {
		return emptyCredentialProvider
	}

	raw, routing, exists, err := loadDockerCredentialConfig(path)
	if err != nil {
		return failedCredentialProvider(err)
	}

	if !exists {
		return emptyCredentialProvider
	}

	store, err := orascredentials.NewMemoryStoreFromDockerConfig(raw)
	if err != nil {
		return failedCredentialProvider(errDockerCredentials)
	}

	return func(ctx context.Context, registryName string) (Credentials, error) {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return Credentials{}, fmt.Errorf("read Docker credentials: %w", ctxErr)
		}

		if helperConfigured(routing, registryName) {
			return Credentials{}, errDockerCredentials
		}

		value, err := storedCredential(ctx, store, registryName)
		if err != nil || credentialSize(value) > maximumCredentialBytes {
			return Credentials{}, errDockerCredentials
		}

		return Credentials{
			Username:     value.Username,
			Password:     value.Password,
			RefreshToken: value.RefreshToken,
			AccessToken:  value.AccessToken,
		}, nil
	}
}

func loadDockerCredentialConfig(
	path string,
) ([]byte, credentialRouting, bool, error) {
	var emptyRouting credentialRouting

	raw, exists, err := readDockerCredentialConfig(path, maximumDockerConfigBytes)
	if err != nil || !exists {
		return nil, emptyRouting, exists, err
	}

	var routing credentialRouting
	if json.Unmarshal(raw, &routing) != nil || invalidCredentialRouting(routing) {
		return nil, emptyRouting, true, errDockerCredentials
	}

	return raw, routing, true, nil
}

func dockerConfigPath(explicit string) string {
	if explicit != "" {
		return explicit
	}

	if directory := os.Getenv("DOCKER_CONFIG"); directory != "" {
		return filepath.Join(directory, dockerConfigFilename)
	}

	home := os.Getenv("HOME")
	if home == "" {
		return ""
	}

	return filepath.Join(home, dockerConfigDirectory, dockerConfigFilename)
}

func invalidCredentialRouting(routing credentialRouting) bool {
	if routing.Store != nil && *routing.Store == "" {
		return true
	}

	for _, helper := range routing.Helpers {
		if helper == "" {
			return true
		}
	}

	return false
}

func helperConfigured(routing credentialRouting, registryName string) bool {
	for _, server := range credentialRouteKeys(registryName) {
		if _, configured := routing.Helpers[server]; configured {
			return true
		}
	}

	return routing.Store != nil
}

func storedCredential(
	ctx context.Context,
	store orascredentials.Store,
	registryName string,
) (auth.Credential, error) {
	for _, server := range credentialStoreKeys(registryName) {
		value, err := store.Get(ctx, server)
		if err != nil {
			return auth.EmptyCredential, fmt.Errorf("read stored credentials: %w", err)
		}

		if value != auth.EmptyCredential {
			return value, nil
		}
	}

	return auth.EmptyCredential, nil
}

func credentialRouteKeys(registryName string) []string {
	if isDockerRegistry(registryName) {
		return []string{
			"https://index.docker.io/v1/",
			dockerRegistryIndex,
			dockerRegistryName,
			dockerRegistryHost,
		}
	}

	return []string{registryName, "https://" + registryName}
}

func credentialStoreKeys(registryName string) []string {
	if isDockerRegistry(registryName) {
		return []string{dockerRegistryIndex, dockerRegistryName, dockerRegistryHost}
	}

	return []string{registryName}
}

func isDockerRegistry(registryName string) bool {
	return registryName == dockerRegistryName || registryName == dockerRegistryIndex || registryName == dockerRegistryHost
}

func credentialSize(value auth.Credential) int {
	return len(value.Username) + len(value.Password) + len(value.RefreshToken) + len(value.AccessToken)
}

func emptyCredentialProvider(context.Context, string) (Credentials, error) {
	return Credentials{Username: "", Password: "", RefreshToken: "", AccessToken: ""}, nil
}

func failedCredentialProvider(err error) CredentialProvider {
	return func(context.Context, string) (Credentials, error) {
		return Credentials{}, err
	}
}
