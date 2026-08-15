package registry

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"

	"oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

const (
	maximumConfigBytes         = int64(8 << 20)
	maximumManifestBytes       = int64(8 << 20)
	maximumResponseHeaderBytes = 1 << 20
	networkDialTimeout         = 10 * time.Second
	networkKeepAlive           = 30 * time.Second
	registryTimeout            = 30 * time.Second
	responseHeaderTimeout      = 15 * time.Second
	tlsHandshakeTimeout        = 10 * time.Second
)

type credential struct {
	username     string
	password     string
	refreshToken string
	accessToken  string
}

type repositoryFactory func(context.Context, registry.Reference, credential) (remoteRepository, error)

func newRepository(
	reference registry.Reference,
	credentialValue credential,
) (*remote.Repository, error) {
	repository, err := remote.NewRepository(reference.Registry + "/" + reference.Repository)
	if err != nil {
		return nil, fmt.Errorf("create registry repository: %w", err)
	}

	httpClient := newHTTPClient()
	authCredential := new(auth.Credential)
	authCredential.Username = credentialValue.username
	authCredential.Password = credentialValue.password
	authCredential.RefreshToken = credentialValue.refreshToken
	authCredential.AccessToken = credentialValue.accessToken

	authClient := new(auth.Client)
	authClient.Client = httpClient
	authClient.Header = http.Header{
		"User-Agent": {"maniud"},
	}
	authClient.Credential = auth.StaticCredential(reference.Registry, *authCredential)
	authClient.Cache = auth.NewCache()
	repository.Client = authClient
	repository.ManifestMediaTypes = acceptedManifestMediaTypes()
	repository.MaxMetadataBytes = maximumManifestBytes

	return repository, nil
}

func newHTTPClient() *http.Client {
	// The standard library documents DefaultTransport as a *http.Transport.
	//nolint:forcetypeassert // Cloning it preserves standard proxy and connection behavior.
	transport := http.DefaultTransport.(*http.Transport).Clone()
	dialer := new(net.Dialer)
	dialer.Timeout = networkDialTimeout
	dialer.KeepAlive = networkKeepAlive
	transport.DialContext = dialer.DialContext
	tlsConfig := new(tls.Config)
	tlsConfig.MinVersion = tls.VersionTLS12
	transport.TLSClientConfig = tlsConfig
	transport.TLSHandshakeTimeout = tlsHandshakeTimeout
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.MaxResponseHeaderBytes = maximumResponseHeaderBytes

	client := new(http.Client)
	client.Transport = retry.NewTransport(transport)
	client.CheckRedirect = safeRedirect
	client.Timeout = registryTimeout

	return client
}

func safeRedirect(request *http.Request, previous []*http.Request) error {
	if len(previous) >= 3 || request.URL.Scheme != "https" || request.URL.Host == "" || request.URL.User != nil {
		return ErrProtocol
	}

	if request.URL.Host != previous[0].URL.Host && request.Header.Get("Authorization") != "" {
		return ErrProtocol
	}

	return nil
}
