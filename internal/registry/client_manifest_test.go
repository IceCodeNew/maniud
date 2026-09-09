package registry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

//nolint:cyclop,funlen // The TLS fixture covers independent GET, HEAD, size, and digest controls.
func TestResolverAcceptsLengthlessManifestAndVerifiesContent(t *testing.T) {
	t.Parallel()
	const manifestPath = "/v2/team/api/manifests/latest"
	platform := domain.Platform{OS: testOSLinux, Architecture: testArchitectureAMD64}
	config, configDescriptor := configForTest(t, platform)
	manifest, descriptor := manifestForTest(t, configDescriptor)
	for _, test := range []struct {
		name         string
		lengthless   bool
		digest       string
		headMetadata bool
		body         []byte
		wantError    bool
	}{
		{name: "complete headers", digest: descriptor.Digest.String(), body: manifest},
		{name: "missing GET digest", body: manifest},
		{name: "HEAD fallback control", lengthless: true, headMetadata: true, body: manifest},
		{name: "neither response has length", lengthless: true, digest: descriptor.Digest.String(), body: manifest},
		{name: "neither response has metadata", lengthless: true, body: manifest},
		{name: "lengthless oversized", lengthless: true, wantError: true,
			body: []byte(strings.Repeat(" ", int(maximumManifestBytes)+1))},
		{name: "lengthless digest mismatch", lengthless: true, wantError: true,
			digest: domain.Hash([]byte("different manifest")).String(), body: manifest},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			mux.HandleFunc(manifestPath, func(response http.ResponseWriter, request *http.Request) {
				if request.Method == http.MethodHead && test.headMetadata {
					writeRegistryContent(response, manifest, descriptor)

					return
				}
				response.Header().Set("Content-Type", descriptor.MediaType)
				response.Header().Set("Docker-Content-Digest", test.digest)
				if !test.lengthless {
					response.Header().Set("Content-Length", strconv.Itoa(len(test.body)))
				}
				if err := http.NewResponseController(response).Flush(); err != nil {
					t.Error(err)
				}
				_, _ = response.Write(test.body)
			})
			mux.HandleFunc("/v2/team/api/blobs/"+configDescriptor.Digest.String(),
				func(response http.ResponseWriter, _ *http.Request) {
					writeRegistryContent(response, config, toOCIDescriptor(configDescriptor))
				})
			server := httptest.NewTLSServer(mux)
			defer server.Close()
			resolver := newResolver(testRepositoryFactory(t, server.Client()),
				func(context.Context, string) (Credentials, error) { return Credentials{}, nil })
			source := sourceForTest(t, strings.TrimPrefix(server.URL, "https://")+"/team/api")
			result, err := resolver.Resolve(t.Context(), source, platform)
			if test.wantError {
				if !errors.Is(err, ErrProtocol) || result.ReferenceDigest != (domain.Digest{}) {
					t.Fatalf("invalid response accepted: %+v, %v", result, err)
				}

				return
			}
			if err != nil || result.ReferenceDigest.String() != descriptor.Digest.String() {
				t.Fatalf("Resolve = %v; digest = %s", err, result.ReferenceDigest.String())
			}
		})
	}
}

type manifestTestClient func(*http.Request) (*http.Response, error)

func (client manifestTestClient) Do(request *http.Request) (*http.Response, error) {
	return client(request)
}

func TestManifestResponsePreservesReadAndCloseFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		readErr  error
		closeErr error
	}{
		{name: "cancelled read", readErr: context.Canceled},
		{name: "incomplete read", readErr: io.ErrUnexpectedEOF},
		{name: "failed close", closeErr: io.ErrClosedPipe},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := &controlledReader{reader: strings.NewReader("partial"), readErr: test.readErr, closeErr: test.closeErr}
			client := manifestResponseClient{Client: manifestTestClient(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, ContentLength: -1, Body: body}, nil
			})}
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet,
				"https://registry.test/v2/a/manifests/b", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Do(request)
			if response != nil {
				_ = response.Body.Close()
				t.Fatalf("unexpected response: %v", response)
			}
			if !errors.Is(err, ErrProtocol) || !body.closed {
				t.Fatalf("incomplete response=%v, error=%v, closed=%t", response, err, body.closed)
			}
			cause := test.readErr
			if cause == nil {
				cause = test.closeErr
			}
			if !errors.Is(err, cause) {
				t.Fatalf("lost cause %v: %v", cause, err)
			}
		})
	}
}

func TestManifestResponsePreservesCancelledRequest(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://registry.test/v2/a/manifests/b", nil)
	if err != nil {
		t.Fatal(err)
	}
	client := manifestResponseClient{Client: newHTTPClient()}
	response, err := client.Do(request)
	if response != nil {
		_ = response.Body.Close()
		t.Fatalf("unexpected response: %v", response)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request = %v, %v", response, err)
	}
}
