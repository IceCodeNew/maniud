package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/authconfig"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
)

var (
	errImagePullAuth      = errors.New("image pull auth failure with secret")
	errImagePullTransport = errors.New("image pull transport failure")
	errImagePullClose     = errors.New("image pull close failure")
)

type imagePullAuthFunc func(context.Context, imageref.Reference) (credential.Value, error)

func (function imagePullAuthFunc) Credentials(
	ctx context.Context,
	reference imageref.Reference,
) (credential.Value, error) {
	return function(ctx, reference)
}

type imagePullErrorBody struct {
	io.Reader

	closeErr error
}

func (body imagePullErrorBody) Close() error {
	return body.closeErr
}

type imagePullReaderFunc func([]byte) (int, error)

func (function imagePullReaderFunc) Read(buffer []byte) (int, error) {
	return function(buffer)
}

func TestPullImageSendsImmutableAuthenticatedRequestAndConsumesStream(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	authenticator := imagePullAuthFunc(func(context.Context, imageref.Reference) (credential.Value, error) {
		return credential.Value{
			Username:     "robot",
			Password:     "password",
			RefreshToken: "refresh-token",
			AccessToken:  "access-token",
		}, nil
	})

	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertImagePullRequest(t, request, expected.ReferenceDigest.String())
		response.Header().Set(contentTypeHeader, jsonContentType+"; charset=utf-8")
		_, _ = io.WriteString(response,
			`{"status":"Pulling manifest","id":"sha256:layer","progress":"1 B / 2 B",`+
				`"progressDetail":{"current":1,"total":2,"start":0,"hidecounts":false,"units":"bytes"}}`+"\n"+
				`{"stream":"done","status":"Pull complete","aux":{"ID":"sha256:image"}}`+"\n",
		)
	}))

	err := client.PullImage(context.Background(), expected, authenticator)
	if err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}
}

func TestPullImageSupportsAnonymousVariantPulls(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	expected.Platform.Architecture = dockerArchitectureARM64
	expected.Platform.Variant = dockerARM64Variant

	authenticator := imagePullAuthFunc(func(
		_ context.Context,
		reference imageref.Reference,
	) (credential.Value, error) {
		if reference.String() != expected.Reference {
			t.Fatal("pull authenticator received another reference")
		}

		return credential.Value{
			Username: "", Password: "", RefreshToken: "", AccessToken: "",
		}, nil
	})
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		auth, err := authconfig.Decode(request.Header.Get(dockerRegistryAuthHeader))
		if err != nil || request.URL.Query().Get(imagePullPlatformQuery) != "linux/arm64/v8" ||
			auth.ServerAddress != "example.com" || auth.Username != "" || auth.Password != "" {
			t.Fatal("anonymous variant pull request is invalid")
		}

		response.Header().Set(contentTypeHeader, jsonContentType)
	}))
	client.version.Architecture = dockerArchitectureARM64

	err := client.PullImage(context.Background(), expected, authenticator)
	if err != nil {
		t.Fatalf("PullImage(anonymous) error = %v", err)
	}
}

func TestImagePullRequestOmitsEmptyAuthentication(t *testing.T) {
	t.Parallel()

	client := connectedTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	request, err := client.imagePullRequest(context.Background(), "/images/create", nil, "")
	if err != nil {
		t.Fatalf("imagePullRequest() error = %v", err)
	}
	if request.Header.Get(dockerRegistryAuthHeader) != "" {
		t.Fatal("imagePullRequest() set empty registry authentication")
	}
}

func TestPullImageRejectsInvalidInputAndAuthentication(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	authenticator := testImagePullAuthenticator()

	invalid := expected
	invalid.Reference = testInvalidLiteral
	assertImagePullError(t, connectedTestClient(t, nil), invalid, authenticator, ErrUnsupportedWorkload)
	assertImagePullError(t, nil, expected, authenticator, ErrUnsupportedWorkload)
	assertImagePullError(t, connectedTestClient(t, nil), expected, nil, ErrUnavailable)

	invalidVersion := connectedTestClient(t, nil)
	invalidVersion.version.Protocol = ""
	assertImagePullError(t, invalidVersion, expected, authenticator, ErrProtocol)

	invalidEndpoint := connectedTestClient(t, nil)
	invalidEndpoint.baseURL.Host = "invalid\nhost"
	assertImagePullError(t, invalidEndpoint, expected, authenticator, ErrProtocol)

	tests := []struct {
		name string
		auth imagePullAuthFunc
		want error
	}{
		{
			name: "provider failure",
			auth: func(context.Context, imageref.Reference) (credential.Value, error) {
				return credential.Value{}, errImagePullAuth
			},
			want: ErrUnavailable,
		},
		{
			name: "invalid UTF-8",
			auth: staticImagePullAuth(credential.Value{
				Username: "", Password: string([]byte{0xff}), RefreshToken: "", AccessToken: "",
			}),
			want: ErrProtocol,
		},
		{
			name: testOversizedCase,
			auth: staticImagePullAuth(credential.Value{
				Username: "", Password: strings.Repeat("x", maximumImagePullCredential+1),
				RefreshToken: "", AccessToken: "",
			}),
			want: ErrProtocol,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := connectedTestClient(t, nil).PullImage(context.Background(), expected, test.auth)
			if !errors.Is(err, test.want) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("PullImage() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPullImageContainsTransportCancellationAndResponseFailures(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	authenticator := testImagePullAuthenticator()
	transportFailure := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errImagePullTransport
	}))
	transportFailure.version = testDockerVersion()

	assertImagePullError(t, transportFailure, expected, authenticator, ErrUnavailable)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err := transportFailure.PullImage(cancelled, expected, authenticator)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PullImage(cancelled) error = %v", err)
	}

	tests := []struct {
		name        string
		status      int
		contentType string
	}{
		{name: testStatusCase, status: http.StatusUnauthorized, contentType: jsonContentType},
		{name: testContentTypeCase, status: http.StatusOK, contentType: plainTextContentType},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set(contentTypeHeader, test.contentType)
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, `{"message":"contains private upstream detail"}`)
			}))

			err := client.PullImage(context.Background(), expected, authenticator)
			if !errors.Is(err, ErrProtocol) || strings.Contains(err.Error(), "private") {
				t.Fatalf("PullImage(%s) error = %v", test.name, err)
			}
		})
	}
}

func TestPullImageHonorsCallerDeadline(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	authenticator := testImagePullAuthenticator()
	client := testClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		<-request.Context().Done()

		return nil, request.Context().Err()
	}))
	client.version = testDockerVersion()

	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()

	err := client.PullImage(ctx, expected, authenticator)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("PullImage(deadline) error = %v", err)
	}
}

func TestPullImageContainsDaemonErrorsAndCloseFailures(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	authenticator := testImagePullAuthenticator()
	secret := "private daemon diagnostic"
	daemonFailure := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response,
			`{"error":"`+secret+`","errorDetail":{"code":401,"message":"`+secret+`"}}`,
		)
	}))

	err := daemonFailure.PullImage(context.Background(), expected, authenticator)
	if err == nil || !errors.Is(err, ErrImagePull) || strings.Contains(err.Error(), secret) {
		t.Fatalf("PullImage(daemon failure) error = %v", err)
	}

	closeFailure := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{ //nolint:exhaustruct // The pull consumer needs only status, header, and body.
			StatusCode: http.StatusOK,
			Header:     http.Header{contentTypeHeader: {jsonContentType}},
			Body: imagePullErrorBody{
				Reader:   strings.NewReader(`{"status":"complete"}`),
				closeErr: errImagePullClose,
			},
		}, nil
	}))
	closeFailure.version = testDockerVersion()

	assertImagePullError(t, closeFailure, expected, authenticator, ErrUnavailable)
}

func TestConsumeImagePullResponseReturnsCancellationDuringStreamRead(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	body := imagePullErrorBody{
		Reader: imagePullReaderFunc(func([]byte) (int, error) {
			cancel()

			return 0, io.ErrUnexpectedEOF
		}),
		closeErr: nil,
	}
	response := &http.Response{ //nolint:exhaustruct // The pull consumer needs only status, header, and body.
		StatusCode: http.StatusOK,
		Header:     http.Header{contentTypeHeader: {jsonContentType}},
		Body:       body,
	}

	err := consumeImagePullResponse(ctx, ctx, response)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("consumeImagePullResponse(cancelled) error = %v", err)
	}
}

func TestImagePullRequestErrorReturnsPullDeadline(t *testing.T) {
	t.Parallel()

	pullContext, cancel := context.WithCancel(context.Background())
	cancel()

	err := imagePullRequestError(context.Background(), pullContext)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("imagePullRequestError() = %v", err)
	}
}

func TestDecodeImagePullStreamAcceptsDocumentedFramesAndCleanEOF(t *testing.T) {
	t.Parallel()

	streams := []string{
		"",
		`{"status":"complete"}`,
		`{"id":"layer","progressDetail":{"current":1,"total":2,"start":0,"hidecounts":true,"units":"bytes"}}`,
		`{"stream":"done","progress":"complete","aux":{"ID":"sha256:image"}}`,
	}

	for _, stream := range streams {
		err := decodeImagePullStream(strings.NewReader(stream))
		if err != nil {
			t.Fatalf("decodeImagePullStream(%q) error = %v", stream, err)
		}
	}
}

func TestDecodeImagePullStreamRejectsInvalidOrUnboundedFrames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stream io.Reader
		want   error
	}{
		{name: testMalformedCase, stream: strings.NewReader(`{"status":`), want: ErrProtocol},
		{name: "trailing malformed", stream: strings.NewReader(`{"status":"ok"}{`), want: ErrProtocol},
		{name: "duplicate", stream: strings.NewReader(`{"status":"a","status":"b"}`), want: ErrProtocol},
		{name: "nested duplicate", stream: strings.NewReader(`{"aux":{"id":1,"id":2}}`), want: ErrProtocol},
		{name: "extra field", stream: strings.NewReader(`{"status":"ok","unknown":true}`), want: ErrProtocol},
		{name: "empty frame", stream: strings.NewReader(`{}`), want: ErrProtocol},
		{name: "null frame", stream: strings.NewReader(`null`), want: ErrProtocol},
		{name: "invalid UTF-8", stream: strings.NewReader("{\"status\":\"\xff\"}"), want: ErrProtocol},
		{
			name: "oversized value",
			stream: strings.NewReader(
				`{"status":"` + strings.Repeat("x", maximumImagePullValueBytes+1) + `"}`,
			),
			want: ErrProtocol,
		},
		{
			name:   "invalid progress",
			stream: strings.NewReader(`{"status":"ok","progressDetail":{"current":2,"total":1}}`),
			want:   ErrProtocol,
		},
		{name: "invalid value", stream: strings.NewReader(`{"status":"line\nfeed"}`), want: ErrProtocol},
		{
			name:   "legacy error only",
			stream: strings.NewReader(`{"error":"failed"}`),
			want:   ErrProtocol,
		},
		{
			name:   "mismatched error",
			stream: strings.NewReader(`{"error":"one","errorDetail":{"code":1,"message":"two"}}`),
			want:   ErrProtocol,
		},
		{
			name:   "invalid error",
			stream: strings.NewReader(`{"errorDetail":{"code":-1,"message":"failed"}}`),
			want:   ErrProtocol,
		},
		{name: "read failure", stream: errorReader{}, want: ErrProtocol},
	}
	assertInvalidImagePullStreams(t, tests)
}

func TestDecodeImagePullStreamRejectsBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stream io.Reader
		want   error
	}{
		{
			name: "oversized frame",
			stream: strings.NewReader(
				`{"status":"` + strings.Repeat("x", int(maximumImagePullFrameBytes)) + `"}`,
			),
			want: ErrProtocol,
		},
		{
			name:   "oversized stream",
			stream: strings.NewReader(strings.Repeat(" ", int(maximumImagePullBytes+1))),
			want:   ErrProtocol,
		},
		{
			name:   "too many frames",
			stream: strings.NewReader(strings.Repeat(`{"status":"ok"}`, maximumImagePullFrames+1)),
			want:   ErrProtocol,
		},
	}
	assertInvalidImagePullStreams(t, tests)
}

func assertInvalidImagePullStreams(
	t *testing.T,
	tests []struct {
		name   string
		stream io.Reader
		want   error
	},
) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := decodeImagePullStream(test.stream)
			if !errors.Is(err, test.want) {
				t.Fatalf("decodeImagePullStream() error = %v, want %v", err, test.want)
			}
		})
	}
}

func assertImagePullRequest(t *testing.T, request *http.Request, digest string) {
	t.Helper()

	if !validImagePullRequestTarget(request, digest) || !validImagePullRequestAuth(request) {
		t.Fatal("image pull request is invalid")
	}
}

func validImagePullRequestTarget(request *http.Request, digest string) bool {
	query := request.URL.Query()
	validTarget := request.Method == http.MethodPost && request.URL.Path == "/v1.55/images/create" &&
		query.Get("fromImage") == "example.com/team/api" && query.Get("tag") == digest &&
		query.Get(imagePullPlatformQuery) == "linux/amd64"

	return validTarget && request.Header.Get("Accept") == jsonContentType && request.ContentLength == 0
}

func validImagePullRequestAuth(request *http.Request) bool {
	auth, err := authconfig.Decode(request.Header.Get(dockerRegistryAuthHeader))
	if err != nil || auth == nil {
		return false
	}

	return auth.ServerAddress == "example.com" && auth.Username == "robot" &&
		auth.Password == "password" && auth.IdentityToken == "refresh-token" &&
		auth.RegistryToken == "access-token" && auth.Auth == ""
}

func assertImagePullError(
	t *testing.T,
	client *Client,
	expected domain.ImageIdentity,
	authenticator credential.Provider,
	want error,
) {
	t.Helper()

	err := client.PullImage(context.Background(), expected, authenticator)
	if !errors.Is(err, want) {
		t.Fatalf("PullImage() error = %v, want %v", err, want)
	}
}

func staticImagePullAuth(credentials credential.Value) imagePullAuthFunc {
	return func(context.Context, imageref.Reference) (credential.Value, error) {
		return credentials, nil
	}
}

func testImagePullAuthenticator() imagePullAuthFunc {
	return staticImagePullAuth(credential.Value{
		Username:     "",
		Password:     "",
		RefreshToken: "",
		AccessToken:  "",
	})
}
