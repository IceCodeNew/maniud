package registry

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"oras.land/oras-go/v2/registry/remote/auth"
)

func TestDockerCredentialProviderReadsStaticCredentials(t *testing.T) {
	t.Parallel()

	path := writeCredentialConfig(t, `{
  "auths": {
    "registry.example": {
      "auth": "`+base64.StdEncoding.EncodeToString([]byte(testRegistryUsername+":"+testRegistrySecret))+`",
      "identitytoken": "`+testRefreshToken+`",
      "registrytoken": "`+testAccessToken+`"
    },
    "https://index.docker.io/v1/": {
      "auth": "`+base64.StdEncoding.EncodeToString([]byte("hub:token"))+`"
    }
  }
}`)

	provider := dockerCredentialProvider(path)

	got, err := provider(context.Background(), "registry.example")
	if err != nil {
		t.Fatalf("provider(registry.example) error = %v", err)
	}

	want := Credentials{
		Username:     testRegistryUsername,
		Password:     testRegistrySecret,
		RefreshToken: testRefreshToken,
		AccessToken:  testAccessToken,
	}
	if got != want {
		t.Fatalf("provider(registry.example) = %#v", got)
	}

	hub, err := provider(context.Background(), "docker.io")
	if err != nil || hub.Username != "hub" || hub.Password != "token" {
		t.Fatalf("provider(docker.io) = %#v, %v", hub, err)
	}

	empty, err := provider(context.Background(), "missing.example")
	if err != nil || empty != (Credentials{}) {
		t.Fatalf("provider(missing.example) = %#v, %v", empty, err)
	}
}

func TestDockerCredentialProviderDefersConfiguredHelpers(t *testing.T) {
	t.Parallel()

	perRegistry := dockerCredentialProvider(writeCredentialConfig(t, `{
  "auths": {"registry.example": {"auth": "cm9ib3Q6c2VjcmV0"}},
  "credHelpers": {"https://registry.example": "pass"}
}`))

	_, err := perRegistry(context.Background(), "registry.example")
	if !errors.Is(err, errDockerCredentials) {
		t.Fatalf("per-registry helper error = %v", err)
	}

	got, err := perRegistry(context.Background(), "other.example")
	if err != nil || got != (Credentials{}) {
		t.Fatalf("unrelated registry = %#v, %v", got, err)
	}

	global := dockerCredentialProvider(writeCredentialConfig(t, `{"credsStore":"osxkeychain"}`))

	_, err = global(context.Background(), "registry.example")
	if !errors.Is(err, errDockerCredentials) {
		t.Fatalf("global helper error = %v", err)
	}
}

func TestDockerCredentialProviderRejectsInvalidConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "duplicate", content: `{"auths":{},"auths":{}}`},
		{name: "invalid auth", content: `{"auths":{"registry.example":{"auth":"%%%"}}}`},
		{name: "invalid helper type", content: `{"credHelpers":{"registry.example":1}}`},
		{name: "empty helper", content: `{"credHelpers":{"registry.example":""}}`},
		{name: "empty store", content: `{"credsStore":""}`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			provider := dockerCredentialProvider(writeCredentialConfig(t, test.content))

			_, err := provider(context.Background(), "registry.example")
			if !errors.Is(err, errDockerCredentials) {
				t.Fatalf("provider() error = %v", err)
			}
		})
	}
}

func TestDockerCredentialProviderRejectsUnsafeConfigFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()

	insecure := filepath.Join(directory, "insecure.json")

	err := os.WriteFile(insecure, []byte(`{}`), 0o644) //nolint:gosec // Intentional broad-mode fixture.
	if err != nil {
		t.Fatalf("WriteFile(insecure) error = %v", err)
	}

	target := filepath.Join(directory, "target.json")

	err = os.WriteFile(target, []byte(`{}`), 0o600)
	if err != nil {
		t.Fatalf("WriteFile(target) error = %v", err)
	}

	symlink := filepath.Join(directory, "link.json")

	err = os.Symlink(target, symlink)
	if err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	oversized := filepath.Join(directory, "oversized.json")
	oversizedContent := []byte(`{"padding":"` + strings.Repeat("x", int(maximumDockerConfigBytes)) + `"}`)

	err = os.WriteFile(oversized, oversizedContent, 0o600)
	if err != nil {
		t.Fatalf("WriteFile(oversized) error = %v", err)
	}

	for _, path := range []string{insecure, symlink, oversized, directory} {
		provider := dockerCredentialProvider(path)

		_, err = provider(context.Background(), "registry.example")
		if !errors.Is(err, errDockerCredentials) {
			t.Fatalf("provider(%q) error = %v", filepath.Base(path), err)
		}
	}
}

func TestDockerCredentialProviderHandlesMissingConfigAndCancellation(t *testing.T) {
	t.Parallel()

	provider := dockerCredentialProvider(filepath.Join(t.TempDir(), "missing.json"))

	got, err := provider(context.Background(), "registry.example")
	if err != nil || got != (Credentials{}) {
		t.Fatalf("missing config = %#v, %v", got, err)
	}

	provider = dockerCredentialProvider(writeCredentialConfig(t, `{}`))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = provider(ctx, "registry.example")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled provider error = %v", err)
	}
}

func TestDockerCredentialProviderRejectsOversizedCredential(t *testing.T) {
	t.Parallel()

	encoded := base64.StdEncoding.EncodeToString([]byte("robot:" + strings.Repeat("x", maximumCredentialBytes)))

	provider := dockerCredentialProvider(writeCredentialConfig(t, `{"auths":{"registry.example":{"auth":"`+encoded+`"}}}`))

	_, err := provider(context.Background(), "registry.example")
	if !errors.Is(err, errDockerCredentials) {
		t.Fatalf("oversized credential error = %v", err)
	}
}

func TestDockerConfigPathPrecedence(t *testing.T) {
	explicit := filepath.Join("explicit", "config.json")
	if got := dockerConfigPath(explicit); got != explicit {
		t.Fatalf("dockerConfigPath(explicit) = %q", got)
	}

	t.Setenv("DOCKER_CONFIG", filepath.Join("docker", "config"))
	t.Setenv("HOME", filepath.Join("home", "ignored"))

	if got := dockerConfigPath(""); got != filepath.Join("docker", "config", dockerConfigFilename) {
		t.Fatalf("dockerConfigPath(DOCKER_CONFIG) = %q", got)
	}

	t.Setenv("DOCKER_CONFIG", "")

	if got := dockerConfigPath(""); got != filepath.Join("home", "ignored", dockerConfigDirectory, dockerConfigFilename) {
		t.Fatalf("dockerConfigPath(HOME) = %q", got)
	}

	t.Setenv("HOME", "")

	if got := dockerConfigPath(""); got != "" {
		t.Fatalf("dockerConfigPath(no home) = %q", got)
	}

	provider := dockerCredentialProvider("")

	got, err := provider(context.Background(), "registry.example")
	if err != nil || got != (Credentials{}) {
		t.Fatalf("provider(no config path) = %#v, %v", got, err)
	}
}

func TestStoredCredentialHandlesFallbackAndStoreFailure(t *testing.T) {
	t.Parallel()

	store := &credentialStoreFixture{
		values: map[string]auth.Credential{
			dockerRegistryHost: {Username: testRegistryUsername, Password: testRegistrySecret},
		},
	}

	got, err := storedCredential(context.Background(), store, "docker.io")
	if err != nil || got.Username != testRegistryUsername {
		t.Fatalf("storedCredential(fallback) = %#v, %v", got, err)
	}

	store.err = io.ErrClosedPipe

	_, err = storedCredential(context.Background(), store, "registry.example")
	if err == nil {
		t.Fatal("storedCredential(store failure) succeeded")
	}
}

func writeCredentialConfig(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), dockerConfigFilename)

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	return path
}

type credentialStoreFixture struct {
	values map[string]auth.Credential
	err    error
}

func (store *credentialStoreFixture) Get(_ context.Context, server string) (auth.Credential, error) {
	if store.err != nil {
		return auth.EmptyCredential, store.err
	}

	if value, found := store.values[server]; found {
		return value, nil
	}

	return auth.EmptyCredential, nil
}

func (*credentialStoreFixture) Put(context.Context, string, auth.Credential) error {
	return nil
}

func (*credentialStoreFixture) Delete(context.Context, string) error {
	return nil
}
