package docker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
)

const (
	minimumAPIVersion = "1.54"
	maximumAPIVersion = "1.55"
	maximumPingBytes  = 1024
	maximumJSONBytes  = 1 << 20
	jsonContentType   = "application/json"
	apiVersionHeader  = "Api-Version"
	httpScheme        = "http"
)

var (
	// ErrUnavailable reports a transport failure without exposing endpoint details.
	ErrUnavailable = errors.New("docker engine is unavailable")
	// ErrProtocol reports an incompatible or malformed Docker Engine response.
	ErrProtocol = errors.New("docker engine protocol is invalid")
)

// Version is the runtime-neutral result of Docker API negotiation.
type Version struct {
	Protocol     string
	Minimum      string
	Maximum      string
	Product      string
	OS           string
	Architecture string
}

// Client is a Docker Engine connection fixed to one negotiated API version.
type Client struct {
	httpClient *http.Client
	baseURL    url.URL
	version    Version
}

// Connect negotiates the supported Docker API range and returns an immutable client.
func Connect(ctx context.Context, endpoint Endpoint) (*Client, Version, error) {
	var emptyVersion Version

	if endpoint.transport == nil || endpoint.baseURL.Scheme == "" || endpoint.baseURL.Host == "" {
		return nil, emptyVersion, ErrInvalidEndpoint
	}

	httpClient := &http.Client{
		Transport: endpoint.transport.Clone(),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Jar:     nil,
		Timeout: requestTimeout,
	}
	client := &Client{httpClient: httpClient, baseURL: endpoint.baseURL, version: emptyVersion}

	version, err := client.negotiate(ctx)
	if err != nil {
		httpClient.CloseIdleConnections()

		return nil, emptyVersion, err
	}

	client.version = version

	return client, version, nil
}

// Version returns the negotiated immutable protocol and daemon version fields.
func (client *Client) Version() Version {
	return client.version
}

// CloseIdleConnections closes pooled transport connections.
func (client *Client) CloseIdleConnections() {
	if client.httpClient != nil {
		client.httpClient.CloseIdleConnections()
	}
}

func (client *Client) request(ctx context.Context, method, path string) (*http.Response, error) {
	endpoint := client.baseURL
	endpoint.Path = path

	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), nil)
	if err != nil {
		return nil, ErrProtocol
	}

	request.Header.Set("Accept", jsonContentType)

	response, err := client.httpClient.Do(request)
	if err != nil {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return nil, fmt.Errorf("docker engine request: %w", ctxErr)
		}

		return nil, ErrUnavailable
	}

	return response, nil
}
