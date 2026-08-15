package registry

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

func TestNewRepositoryUsesExplicitCredentialsAndTLS(t *testing.T) {
	t.Parallel()

	reference, err := registry.ParseReference("docker.io/library/api:1")
	if err != nil {
		t.Fatalf("ParseReference() error = %v", err)
	}

	wantCredential := credential{
		username:     testUsername,
		password:     "password",
		refreshToken: "refresh",
		accessToken:  "access",
	}

	value, err := newRepository(reference, wantCredential)
	if err != nil {
		t.Fatalf("newRepository() error = %v", err)
	}

	assertRepositoryConfiguration(t, value)
	assertRepositoryCredential(t, value, wantCredential)
}

func assertRepositoryConfiguration(t *testing.T, repository *remote.Repository) {
	t.Helper()

	if repository.PlainHTTP || repository.Reference.Registry != "docker.io" ||
		repository.Reference.Repository != "library/api" || repository.MaxMetadataBytes != maximumManifestBytes ||
		!slices.Equal(repository.ManifestMediaTypes, acceptedManifestMediaTypes()) {
		t.Fatalf("newRepository() = %#v", repository)
	}
}

func assertRepositoryCredential(t *testing.T, repository *remote.Repository, want credential) {
	t.Helper()

	authClient, valid := repository.Client.(*auth.Client)
	if !valid {
		t.Fatalf("repository client type = %T", repository.Client)
	}

	got, err := authClient.Credential(context.Background(), "registry-1.docker.io")
	if err != nil || got.Username != want.username || got.Password != want.password ||
		got.RefreshToken != want.refreshToken || got.AccessToken != want.accessToken {
		t.Fatalf("credential = %#v, %v", got, err)
	}
}

func TestNewRepositoryRejectsInvalidReference(t *testing.T) {
	t.Parallel()

	_, err := newRepository(
		registry.Reference{Registry: "bad host", Repository: testImageName},
		credential{},
	)
	if err == nil {
		t.Fatal("newRepository() accepted invalid reference")
	}
}

func TestRegistryHTTPClientPolicy(t *testing.T) {
	t.Parallel()

	client := newHTTPClient()
	if client.Timeout != registryTimeout || client.CheckRedirect == nil {
		t.Fatalf("newHTTPClient() = %#v", client)
	}

	retryTransport, valid := client.Transport.(*retry.Transport)
	if !valid {
		t.Fatalf("transport type = %T", client.Transport)
	}

	transport, valid := retryTransport.Base.(*http.Transport)
	if !valid || transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion != tls.VersionTLS12 ||
		transport.Proxy == nil || transport.DialContext == nil {
		t.Fatalf("base transport = %#v", retryTransport.Base)
	}
}

func TestSafeRedirect(t *testing.T) {
	t.Parallel()

	origin := &http.Request{URL: mustURL(t, "https://registry.example/v2/")}

	tests := []struct {
		name     string
		request  *http.Request
		previous []*http.Request
		want     error
	}{
		{
			name:     "same HTTPS origin",
			request:  &http.Request{URL: mustURL(t, "https://registry.example/v2/team/api")},
			previous: []*http.Request{origin},
		},
		{
			name:     "HTTP downgrade",
			request:  &http.Request{URL: mustURL(t, "http://registry.example/v2/team/api")},
			previous: []*http.Request{origin},
			want:     ErrProtocol,
		},
		{
			name: "authorization crosses host",
			request: &http.Request{
				URL:    mustURL(t, "https://other.example/v2/team/api"),
				Header: http.Header{"Authorization": {"Bearer secret"}},
			},
			previous: []*http.Request{origin},
			want:     ErrProtocol,
		},
		{
			name:     "too many redirects",
			request:  &http.Request{URL: mustURL(t, "https://registry.example/v2/team/api")},
			previous: []*http.Request{origin, origin, origin},
			want:     ErrProtocol,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := safeRedirect(test.request, test.previous)
			if !errors.Is(err, test.want) {
				t.Fatalf("safeRedirect() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestResolverRegistryBasicAuthentication(t *testing.T) {
	t.Parallel()

	platform := Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	configRaw, configDescriptor := configForTest(t, platform)
	manifestRaw, manifestDescriptor := manifestForTest(t, configDescriptor)
	server, requests := newAuthenticatedRegistryServer(
		t,
		testUsername,
		"password",
		manifestRaw,
		manifestDescriptor,
		configRaw,
		toOCIDescriptor(configDescriptor),
	)

	host := strings.TrimPrefix(server.URL, "https://")
	options := Options{
		Credentials: func(context.Context, string) (Credentials, error) {
			return Credentials{Username: testUsername, Password: "password"}, nil
		},
	}
	resolver := newResolver(testRepositoryFactory(t, server.Client()), options.Credentials)

	result, err := resolver.Resolve(
		context.Background(),
		sourceForTest(t, host+"/team/api"),
		platform,
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if result.Reference.Digest().String() != manifestDescriptor.Digest.String() ||
		result.ImageConfig.String() != configDescriptor.Digest.String() {
		t.Fatalf("Resolve() = %#v", result)
	}

	for _, path := range requests() {
		if strings.Contains(path, "/blobs/") && !strings.HasSuffix(path, configDescriptor.Digest.String()) {
			t.Fatalf("resolver fetched layer path %q", path)
		}
	}
}

func TestResolverRegistryAuthenticationFailure(t *testing.T) {
	t.Parallel()

	platform := Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	configRaw, configDescriptor := configForTest(t, platform)
	manifestRaw, manifestDescriptor := manifestForTest(t, configDescriptor)
	server, _ := newAuthenticatedRegistryServer(
		t,
		testUsername,
		"password",
		manifestRaw,
		manifestDescriptor,
		configRaw,
		toOCIDescriptor(configDescriptor),
	)
	host := strings.TrimPrefix(server.URL, "https://")
	resolver := newResolver(
		testRepositoryFactory(t, server.Client()),
		func(context.Context, string) (Credentials, error) {
			return Credentials{Username: testUsername, Password: "wrong"}, nil
		},
	)

	_, err := resolver.Resolve(context.Background(), sourceForTest(t, host+"/team/api"), platform)
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func testRepositoryFactory(t *testing.T, httpClient *http.Client) repositoryFactory {
	t.Helper()

	return func(
		_ context.Context,
		reference registry.Reference,
		value credential,
	) (remoteRepository, error) {
		repository, err := remote.NewRepository(reference.Registry + "/" + reference.Repository)
		if err != nil {
			return nil, fmt.Errorf("create test registry repository: %w", err)
		}

		repository.Client = &auth.Client{
			Client: httpClient,
			Credential: auth.StaticCredential(reference.Registry, auth.Credential{
				Username: value.username,
				Password: value.password,
			}),
			Cache: auth.NewCache(),
		}
		repository.ManifestMediaTypes = acceptedManifestMediaTypes()
		repository.MaxMetadataBytes = maximumManifestBytes

		return repository, nil
	}
}

func newAuthenticatedRegistryServer(
	t *testing.T,
	username string,
	password string,
	manifestRaw []byte,
	manifestDescriptor ocispec.Descriptor,
	configRaw []byte,
	configDescriptor ocispec.Descriptor,
) (*httptest.Server, func() []string) {
	t.Helper()

	wantAuthorization := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))

	var mutex sync.Mutex

	var requests []string

	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		mutex.Lock()

		requests = append(requests, request.URL.Path)
		mutex.Unlock()

		if request.Header.Get("Authorization") != wantAuthorization {
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			response.WriteHeader(http.StatusUnauthorized)
			_, _ = response.Write([]byte(`{"errors":[{"code":"UNAUTHORIZED","message":"denied"}]}`))

			return
		}

		switch request.URL.Path {
		case "/v2/team/api/manifests/latest":
			writeRegistryContent(response, manifestRaw, manifestDescriptor)
		case "/v2/team/api/blobs/" + configDescriptor.Digest.String():
			writeRegistryContent(response, configRaw, configDescriptor)
		default:
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusNotFound)
			_, _ = response.Write([]byte(`{"errors":[{"code":"MANIFEST_UNKNOWN","message":"missing"}]}`))
		}
	}))
	t.Cleanup(server.Close)

	return server, func() []string {
		mutex.Lock()
		defer mutex.Unlock()

		return slices.Clone(requests)
	}
}

func writeRegistryContent(response http.ResponseWriter, raw []byte, descriptorValue ocispec.Descriptor) {
	response.Header().Set("Content-Type", descriptorValue.MediaType)
	response.Header().Set("Docker-Content-Digest", descriptorValue.Digest.String())
	response.WriteHeader(http.StatusOK)
	_, _ = response.Write(raw)
}

func mustURL(t *testing.T, value string) *url.URL {
	t.Helper()

	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatalf("url.Parse(%q) error = %v", value, err)
	}

	return parsed
}
