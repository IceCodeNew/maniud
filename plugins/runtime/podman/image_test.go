package podman

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
)

const (
	podmanReferenceDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	podmanManifestDigest  = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	podmanImageConfig     = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	podmanImageReference  = "registry.example/team/app@" + podmanReferenceDigest
	podmanPlainTextType   = "text/plain"
)

var errPodmanImageTest = errors.New("podman image test failure")

func TestProbeImageProvesIdentityAndAbsence(t *testing.T) {
	t.Parallel()

	expected := podmanExpectedImage(t)
	tests := []struct {
		name  string
		write func(http.ResponseWriter)
		state application.ImageProbeState
	}{
		{
			name: "observed",
			write: func(writer http.ResponseWriter) {
				writePodmanJSON(writer, podmanImageDocument())
			},
			state: application.ImageProbeObserved,
		},
		{
			name: "missing",
			write: func(writer http.ResponseWriter) {
				writer.Header().Set(podmanContentType, podmanJSONType)
				writer.WriteHeader(http.StatusNotFound)
				_, _ = writer.Write([]byte(`{"cause":"image unknown","message":"image unknown","response":404}`))
			},
			state: application.ImageProbeMissing,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := connectedPodmanImageClient(t, func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != libpodPrefix+"/images/"+podmanImageReference+"/json" {
					t.Errorf("image request = %s %s", request.Method, request.URL.String())

					return
				}
				test.write(writer)
			})

			probe, err := client.ProbeImage(context.Background(), expected)
			if err != nil || probe.State != test.state {
				t.Fatalf("ProbeImage() = %#v, %v", probe, err)
			}
			if test.state == application.ImageProbeObserved && probe.Image != (application.ImageEvidence{
				ReferenceDigest: expected.ReferenceDigest, PlatformManifest: expected.PlatformManifest,
				ImageConfig: expected.ImageConfig, Platform: expected.Platform,
			}) {
				t.Fatalf("ProbeImage().Image = %#v", probe.Image)
			}
		})
	}
}

func TestProbeImageRejectsInvalidInputAndWireEvidence(t *testing.T) {
	t.Parallel()

	expected := podmanExpectedImage(t)
	if probe, err := (*Client)(nil).ProbeImage(context.Background(), expected); !errors.Is(err, ErrUnsupportedWorkload) ||
		probe != (application.ImageProbe{}) {
		t.Fatalf("nil ProbeImage() = %#v, %v", probe, err)
	}
	invalid := expected
	invalid.Origin = domain.ImageOriginDockerArchive
	client := connectedPodmanImageClient(t, func(http.ResponseWriter, *http.Request) {
		t.Error("invalid input reached daemon")
	})
	if probe, err := client.ProbeImage(context.Background(), invalid); !errors.Is(err, ErrUnsupportedWorkload) ||
		probe != (application.ImageProbe{}) {
		t.Fatalf("ProbeImage(invalid) = %#v, %v", probe, err)
	}
	client.socket.inode++
	if probe, err := client.ProbeImage(context.Background(), expected); !errors.Is(err, ErrInvalidEndpoint) ||
		probe != (application.ImageProbe{}) {
		t.Fatalf("ProbeImage(changed socket) = %#v, %v", probe, err)
	}

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "status", status: http.StatusCreated, contentType: podmanJSONType, body: `{}`},
		{name: "content type", status: http.StatusOK, contentType: podmanPlainTextType, body: podmanImageDocument()},
		{name: podmanTestDuplicate, status: http.StatusOK, contentType: podmanJSONType, body: `{"Id":"x","Id":"y"}`},
		{name: "incomplete", status: http.StatusOK, contentType: podmanJSONType, body: `{"Id":"x"}`},
		{name: "wrong identity", status: http.StatusOK, contentType: podmanJSONType, body: strings.Replace(
			podmanImageDocument(), podmanImageConfig, podmanReferenceDigest, 1,
		)},
		{name: "text 404", status: http.StatusNotFound, contentType: podmanPlainTextType, body: "not found"},
		{
			name: "invalid 404", status: http.StatusNotFound, contentType: podmanJSONType,
			body: `{"cause":"x","message":"x","response":500}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			wireClient := connectedPodmanImageClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set(podmanContentType, test.contentType)
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			})
			probe, err := wireClient.ProbeImage(context.Background(), expected)
			if !errors.Is(err, ErrProtocol) || probe != (application.ImageProbe{}) {
				t.Fatalf("ProbeImage() = %#v, %v", probe, err)
			}
		})
	}
}

func TestPullImageSendsEphemeralAuthenticationAndConsumesSuccess(t *testing.T) {
	t.Parallel()

	expected := podmanExpectedImage(t)
	authenticator := credentialProviderFunc(func(
		_ context.Context,
		reference imageref.Reference,
	) (credential.Value, error) {
		if reference.String() != podmanImageReference {
			t.Fatalf("credential reference = %q", reference.String())
		}

		return credential.Value{
			Username: "user", Password: "password", RefreshToken: "refresh", AccessToken: "access",
		}, nil
	})
	client := connectedPodmanImageClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != libpodPrefix+"/images/pull" ||
			request.URL.Query().Get("reference") != podmanImageReference {
			t.Errorf("pull request = %s %s", request.Method, request.URL.String())

			return
		}
		encoded := request.Header.Get(registryAuthHeader)
		payload, err := base64.URLEncoding.DecodeString(encoded)
		if err != nil {
			t.Errorf("auth header = %q: %v", encoded, err)

			return
		}
		var auth podmanAuthConfig
		if json.Unmarshal(payload, &auth) != nil || auth != (podmanAuthConfig{
			Username: "user", Password: "password", IdentityToken: "refresh", RegistryToken: "access",
		}) {
			t.Errorf("auth payload = %s", payload)

			return
		}
		identifier := strings.TrimPrefix(podmanImageConfig, "sha256:")
		writePodmanJSON(writer,
			`{"status":"pulling","stream":"pulling"}`+"\n"+
				`{"status":"success","images":["`+podmanImageConfig+`"],"id":"`+identifier+`"}`+"\n",
		)
	})
	if err := client.PullImage(context.Background(), expected, authenticator); err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}
}

func TestPullImageContainsFailuresAndCancellation(t *testing.T) {
	t.Parallel()

	expected := podmanExpectedImage(t)
	client := connectedPodmanImageClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writePodmanJSON(writer, `{"status":"error","error":"private upstream detail"}`)
	})
	err := client.PullImage(context.Background(), expected, emptyCredentialProvider{})
	requirePrivatePodmanErrorRedacted(t, err, ErrImagePull)
	if err := client.PullImage(context.Background(), expected, nil); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("PullImage(nil auth) = %v", err)
	}

	invalid := expected
	invalid.PlatformManifest = domain.Digest{}
	err = client.PullImage(context.Background(), invalid, emptyCredentialProvider{})
	if !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("PullImage(invalid) = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.PullImage(cancelled, expected, emptyCredentialProvider{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PullImage(cancelled) = %v", err)
	}

	providerFailure := credentialProviderFunc(func(context.Context, imageref.Reference) (credential.Value, error) {
		return credential.Value{}, errPodmanImageTest
	})
	err = client.PullImage(context.Background(), expected, providerFailure)
	requirePrivatePodmanErrorRedacted(t, err, ErrUnavailable)

	invalidCredential := credentialProviderFunc(func(context.Context, imageref.Reference) (credential.Value, error) {
		return credential.Value{Password: string([]byte{0xff})}, nil
	})
	if err := client.PullImage(context.Background(), expected, invalidCredential); !errors.Is(err, ErrProtocol) {
		t.Fatalf("PullImage(invalid credential) = %v", err)
	}
}

func requirePrivatePodmanErrorRedacted(t *testing.T, err error, target error) {
	t.Helper()

	if err == nil {
		t.Fatal("PullImage(private error) = nil")

		return
	}
	if !errors.Is(err, target) || strings.Contains(err.Error(), "private") {
		t.Fatalf("PullImage(private error) = %v", err)
	}
}

func TestPodmanImageEvidenceHelpers(t *testing.T) {
	t.Parallel()

	expected := podmanExpectedImage(t)
	reference, err := imageref.Parse(expected.Reference)
	if err != nil {
		t.Fatal(err)
	}
	payload, valid := decodePodmanImage(strings.NewReader(podmanImageDocument()))
	if !valid {
		t.Fatal("decodePodmanImage(valid) rejected")
	}
	if evidence, valid := podmanImageEvidence(payload, reference, expected); !valid ||
		evidence.ReferenceDigest != expected.ReferenceDigest {
		t.Fatalf("podmanImageEvidence() = %#v, %t", evidence, valid)
	}

	invalidPayloads := []imageInspectResponse{
		{},
		{
			ID: podmanImageConfig, Digest: podmanManifestDigest,
			RepoDigests: []string{podmanImageReference}, OS: "windows",
			Architecture: podmanArchAMD64, Config: []byte(`{}`),
		},
		{
			ID: podmanImageConfig, Digest: podmanManifestDigest,
			RepoDigests: []string{podmanTestInvalid}, OS: podmanOSLinux,
			Architecture: podmanArchAMD64, Config: []byte(`{}`),
		},
	}
	for _, invalid := range invalidPayloads {
		if _, valid := podmanImageEvidence(invalid, reference, expected); valid {
			t.Fatalf("podmanImageEvidence(%#v) accepted", invalid)
		}
	}
	otherRepository := "registry.example/other/app@" + podmanManifestDigest
	if !validRepoDigests(
		[]string{otherRepository, podmanImageReference, "registry.example/team/app@" + podmanManifestDigest},
		reference,
		expected.PlatformManifest,
	) {
		t.Fatal("validRepoDigests(other repository) rejected")
	}
	platformReference := "registry.example/team/app@" + expected.PlatformManifest.String()
	if !validRepoDigests([]string{platformReference, podmanImageReference}, reference, expected.PlatformManifest) {
		t.Fatal("validRepoDigests(platform manifest) rejected")
	}
	unexpected := "registry.example/team/app@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	if validRepoDigests([]string{podmanImageReference, unexpected}, reference, expected.PlatformManifest) {
		t.Fatal("validRepoDigests(unexpected manifest) accepted")
	}
}

func TestPodmanImageEvidenceAcceptsTopLevelDigest(t *testing.T) {
	t.Parallel()

	expected := podmanExpectedImage(t)
	reference, err := imageref.Parse(expected.Reference)
	if err != nil {
		t.Fatal(err)
	}
	document := strings.Replace(
		podmanImageDocument(), `"Digest":"`+podmanManifestDigest+`"`,
		`"Digest":"`+podmanReferenceDigest+`"`, 1,
	)
	payload, valid := decodePodmanImage(strings.NewReader(document))
	if !valid {
		t.Fatal("decodePodmanImage(top-level digest) rejected")
	}
	if evidence, valid := podmanImageEvidence(payload, reference, expected); !valid ||
		evidence.PlatformManifest != expected.PlatformManifest {
		t.Fatalf("podmanImageEvidence(top-level digest) = %#v, %t", evidence, valid)
	}
}

func TestPodmanImageValueHelpers(t *testing.T) {
	t.Parallel()

	expected := podmanExpectedImage(t)
	if _, valid := podmanImageID("short"); valid {
		t.Fatal("podmanImageID(short) accepted")
	}
	hexID := strings.TrimPrefix(podmanImageConfig, "sha256:")
	if digest, valid := podmanImageID(hexID); !valid || digest != expected.ImageConfig {
		t.Fatalf("podmanImageID(hex) = %v, %t", digest, valid)
	}
	if decodeRawJSONObject(nil, nil) {
		t.Fatal("decodeRawJSONObject(nil) accepted")
	}
	if evidence := emptyImageEvidence(); evidence != (application.ImageEvidence{
		ReferenceDigest: domain.Digest{}, PlatformManifest: domain.Digest{}, ImageConfig: domain.Digest{},
		Platform: domain.Platform{OS: "", Architecture: "", Variant: ""},
	}) {
		t.Fatalf("emptyImageEvidence() = %#v", evidence)
	}
}

func TestPodmanPullStreamRejectsMalformedOrIncompleteMessages(t *testing.T) {
	t.Parallel()

	success := `{"status":"success","id":"` + podmanImageConfig + `"}`
	if err := decodePodmanPullStream(strings.NewReader(success), true); err != nil {
		t.Fatalf("decodePodmanPullStream(valid) = %v", err)
	}
	legacySuccess := `{"id":"` + podmanImageConfig + `"}`
	if err := decodePodmanPullStream(strings.NewReader(legacySuccess), false); err != nil {
		t.Fatalf("decodePodmanPullStream(legacy) = %v", err)
	}
	tests := []struct {
		name string
		body string
		want error
	}{
		{name: "empty", body: "", want: ErrProtocol},
		{name: "invalid json", body: "{", want: ErrProtocol},
		{name: "unknown field", body: `{"unknown":true}`, want: ErrProtocol},
		{name: "unknown status", body: `{"status":"waiting"}`, want: ErrProtocol},
		{name: "error", body: `{"error":"private"}`, want: ErrImagePull},
		{name: "status error", body: `{"status":"error"}`, want: ErrImagePull},
		{name: "no identity", body: `{"status":"success"}`, want: ErrProtocol},
		{name: "no success", body: `{"id":"` + podmanImageConfig + `"}`, want: ErrProtocol},
		{name: "invalid id", body: `{"status":"success","id":"short"}`, want: ErrProtocol},
		{name: "invalid image", body: `{"status":"success","images":["short"]}`, want: ErrProtocol},
		{name: "invalid progress", body: `{"pullProgress":{"status":"unknown"}}`, want: ErrProtocol},
		{name: "duplicate", body: `{"status":"success","status":"pulling"}`, want: ErrProtocol},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := decodePodmanPullStream(strings.NewReader(test.body), true); !errors.Is(err, test.want) {
				t.Fatalf("decodePodmanPullStream() = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPodmanPullMessageValidators(t *testing.T) {
	t.Parallel()

	progress := imagePullMessage{
		Status:   "pulling",
		Progress: &podmanImagePullProgress{Status: "skipped", Total: -1, ProgressComponentID: podmanImageConfig},
	}
	if !validImagePullMessage(progress) || !validPodmanPullProgress(*progress.Progress) {
		t.Fatal("valid pull progress rejected")
	}
	progress.Progress.Status = ""
	if !validImagePullMessage(progress) {
		t.Fatal("pull progress with omitted status rejected")
	}
	progress.Progress.Total = -2
	if validImagePullMessage(progress) {
		t.Fatal("invalid pull progress accepted")
	}
	for _, message := range []imagePullMessage{
		{ID: strings.Repeat("x", maximumTextBytes+1)},
		{Images: make([]string, sha256HexLength+1)},
		{Images: []string{strings.Repeat("x", maximumTextBytes+1)}},
	} {
		if validImagePullMessage(message) {
			t.Fatalf("validImagePullMessage(%#v) accepted", message)
		}
	}
	limited := &io.LimitedReader{R: strings.NewReader(""), N: 0}
	if _, _, err := decodePodmanPullFrame(json.NewDecoder(limited), limited, 0); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanPullFrame(oversized) = %v", err)
	}
	if err := imagePullRequestError(
		context.Background(), context.Background(), ErrInvalidEndpoint,
	); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("imagePullRequestError(protocol) = %v", err)
	}
}

func connectedPodmanImageClient(t *testing.T, imageHandler http.HandlerFunc) *Client {
	t.Helper()

	negotiation := podmanNegotiationHandler("6.1.0", "5.0.0", "6.1.0")
	path := startPodmanTestServer(t, podmanTestHandler(func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasPrefix(request.URL.Path, libpodPrefix+"/images/") {
			imageHandler(writer, request)

			return
		}
		negotiation.ServeHTTP(writer, request)
	}))
	client, _, err := Connect(context.Background(), path)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)

	return client
}

func podmanExpectedImage(t *testing.T) domain.ImageIdentity {
	t.Helper()

	return domain.ImageIdentity{
		Origin:           domain.ImageOriginRegistry,
		Reference:        podmanImageReference,
		ReferenceDigest:  mustPodmanDigest(t, podmanReferenceDigest),
		PlatformManifest: mustPodmanDigest(t, podmanManifestDigest),
		ImageConfig:      mustPodmanDigest(t, podmanImageConfig),
		Platform:         domain.Platform{OS: "linux", Architecture: "amd64", Variant: ""},
	}
}

func mustPodmanDigest(t *testing.T, value string) domain.Digest {
	t.Helper()
	digest, err := domain.ParseDigest(value)
	if err != nil {
		t.Fatal(err)
	}

	return digest
}

func podmanImageDocument() string {
	return fmt.Sprintf(
		`{"Id":%q,"Digest":%q,"RepoDigests":[%q,%q],"RepoTags":[],`+
			`"Os":"linux","Architecture":"amd64","Size":1,"Config":{},"UnknownExtension":true}`,
		podmanImageConfig,
		podmanManifestDigest,
		podmanImageReference,
		"registry.example/team/app@"+podmanManifestDigest,
	)
}

type credentialProviderFunc func(context.Context, imageref.Reference) (credential.Value, error)

func (provider credentialProviderFunc) Credentials(
	ctx context.Context,
	reference imageref.Reference,
) (credential.Value, error) {
	return provider(ctx, reference)
}

type emptyCredentialProvider struct{}

func (emptyCredentialProvider) Credentials(context.Context, imageref.Reference) (credential.Value, error) {
	return credential.Value{}, nil
}

type pullCloseErrorReader struct {
	io.Reader
}

func (pullCloseErrorReader) Close() error {
	return errPodmanImageTest
}

func TestConsumePodmanPullResponseRejectsProtocolAndCloseFailures(t *testing.T) {
	t.Parallel()

	responses := []*http.Response{
		nil,
		{StatusCode: http.StatusOK, Body: nil, Header: http.Header{podmanContentType: {podmanJSONType}}},
		{
			StatusCode: http.StatusCreated, Body: io.NopCloser(bytes.NewBufferString(`{}`)),
			Header: http.Header{podmanContentType: {podmanJSONType}},
		},
		{
			StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(`{}`)),
			Header: http.Header{podmanContentType: {podmanPlainTextType}},
		},
	}
	strictProtocol := semanticVersion{major: 6, minor: 1}
	for _, response := range responses {
		err := consumePodmanPullResponse(context.Background(), context.Background(), response, strictProtocol)
		if !errors.Is(err, ErrProtocol) {
			t.Fatalf("consumePodmanPullResponse(%#v) = %v", response, err)
		}
	}
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{podmanContentType: {podmanJSONType}},
		Body: pullCloseErrorReader{Reader: strings.NewReader(
			`{"status":"success","id":"` + podmanImageConfig + `"}`,
		)},
	}
	if err := consumePodmanPullResponse(
		context.Background(), context.Background(), response, strictProtocol,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("consumePodmanPullResponse(close) = %v", err)
	}
}

func TestConsumePodmanPullResponseSupportsLegacyAndContextFailures(t *testing.T) {
	t.Parallel()

	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"id":"` + podmanImageConfig + `"}`)),
	}
	legacyProtocol := semanticVersion{major: 4, minor: 3, patch: 1}
	if err := consumePodmanPullResponse(
		context.Background(), context.Background(), response, legacyProtocol,
	); err != nil {
		t.Fatalf("consumePodmanPullResponse(legacy) = %v", err)
	}
	if !validPodmanPullContentType(podmanPlainTextType+"; charset=utf-8", legacyProtocol) {
		t.Fatal("validPodmanPullContentType(legacy text) rejected")
	}
	if validPodmanPullContentType("text/plain; charset", legacyProtocol) {
		t.Fatal("validPodmanPullContentType(malformed) accepted")
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	strictProtocol := semanticVersion{major: 6, minor: 1}
	response = &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{podmanContentType: {podmanJSONType}},
		Body:       io.NopCloser(strings.NewReader("{")),
	}
	if err := consumePodmanPullResponse(
		cancelled, context.Background(), response, strictProtocol,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("consumePodmanPullResponse(cancelled) = %v", err)
	}

	timedOut, cancelTimeout := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelTimeout()
	<-timedOut.Done()
	if err := imagePullRequestError(
		context.Background(), timedOut, errPodmanImageTest,
	); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("imagePullRequestError(timeout) = %v", err)
	}
}
