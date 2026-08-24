package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	imagetypes "github.com/moby/moby/api/types/image"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
)

const (
	testArchiveReference = "example.com/team/archive:1"
	testDockerImageUser  = "service"
)

type archiveProbeFixture struct {
	raw      []byte
	expected domain.ImageIdentity
}

type dockerImageUserFailureTest struct {
	name          string
	expected      domain.ImageIdentity
	specification string
	after         func(http.ResponseWriter)
}

type delayedReadCloser struct {
	delay  time.Duration
	reader io.Reader
}

func (reader *delayedReadCloser) Read(buffer []byte) (int, error) {
	time.Sleep(reader.delay)
	reader.delay = 0

	return reader.reader.Read(buffer) //nolint:wrapcheck // Preserve the test response body's stream errors.
}

func (*delayedReadCloser) Close() error {
	return nil
}

func TestProbeArchiveImageProvesSavedRuntimeImage(t *testing.T) {
	t.Parallel()

	for _, containerdStore := range []bool{false, true} {
		storeName := "classic"
		if containerdStore {
			storeName = "containerd"
		}
		t.Run(storeName, func(t *testing.T) {
			t.Parallel()

			fixture := newArchiveProbeFixture(t)
			var requests atomic.Int32
			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				switch requests.Add(1) {
				case 1, 3:
					assertArchiveInspectRequest(t, request, fixture.expected)
					response.Header().Set(contentTypeHeader, jsonContentType)
					_, _ = io.WriteString(response, archiveInspectDocument(fixture.expected, containerdStore))
				case 2:
					assertArchiveSaveRequest(t, request, fixture.expected)
					response.Header().Set(contentTypeHeader, dockerArchiveContentType)
					_, _ = response.Write(fixture.raw)
				default:
					t.Fatalf("unexpected request %s %s", request.Method, request.URL.String())
				}
			}))

			probe, err := client.ProbeImage(context.Background(), fixture.expected)
			if err != nil || probe.State != application.ImageProbeObserved || !probe.Matches(fixture.expected) {
				t.Fatalf("ProbeImage(archive) = %#v, %v", probe, err)
			}
			if requests.Load() != 3 {
				t.Fatalf("request count = %d", requests.Load())
			}
		})
	}
}

func TestResolveImageUserReadsValidatedSavedFilesystem(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	expected := testImageIdentity(t)
	expected.ImageConfig = fixture.expected.ImageConfig
	expected.Platform = fixture.expected.Platform
	var requests atomic.Int32
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch requests.Add(1) {
		case 1, 3:
			assertImageProbeRequest(t, request, expected)
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, imageInspectDocument(expected, true))
		case 2:
			assertArchiveRequest(t, request, expected, "/v1.54/images/"+expected.Reference+"/get")
			if request.Header.Get("Accept") != dockerArchiveContentType {
				t.Fatalf("archive save Accept = %q", request.Header.Get("Accept"))
			}
			response.Header().Set(contentTypeHeader, dockerArchiveContentType)
			_, _ = response.Write(fixture.raw)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.String())
		}
	}))

	resolved, err := client.ResolveImageUser(t.Context(), expected, testDockerImageUser)
	if err != nil || resolved != "1001:1002" || requests.Load() != 3 {
		t.Fatalf("ResolveImageUser() = %q, %v, requests %d", resolved, err, requests.Load())
	}
	if _, err = (*Client)(nil).ResolveImageUser(t.Context(), expected, testDockerImageUser); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ResolveImageUser(nil client) = %v", err)
	}
}

func TestResolveImageUserReadsDocker29NestedOCIIndex(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixtureWithNestedOCI(t)
	expected := testImageIdentity(t)
	expected.ImageConfig = fixture.expected.ImageConfig
	expected.Platform = fixture.expected.Platform
	var requests atomic.Int32
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch requests.Add(1) {
		case 1, 3:
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, imageInspectDocument(expected, true))
		case 2:
			response.Header().Set(contentTypeHeader, dockerArchiveContentType)
			_, _ = response.Write(fixture.raw)
		default:
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.String())
		}
	}))

	resolved, err := client.ResolveImageUser(t.Context(), expected, testDockerImageUser)
	if err != nil || resolved != "1001:1002" || requests.Load() != 3 {
		t.Fatalf("ResolveImageUser(nested OCI) = %q, %v, requests %d", resolved, err, requests.Load())
	}
}

func TestResolveImageUserRejectsArchiveDriftAndPostProbeFailure(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	matching := testImageIdentity(t)
	matching.ImageConfig = fixture.expected.ImageConfig
	matching.Platform = fixture.expected.Platform
	drifted := matching
	drifted.ImageConfig = domain.Hash([]byte("different config"))
	tests := []dockerImageUserFailureTest{
		{
			name:          "archive identity drift",
			expected:      drifted,
			specification: testDockerImageUser,
		},
		{
			name:          "post-probe failure",
			expected:      matching,
			specification: testDockerImageUser,
			after: func(response http.ResponseWriter) {
				response.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name:          "user resolution",
			expected:      matching,
			specification: "missing-user",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assertDockerImageUserFailure(t, fixture.raw, test)
		})
	}
}

func assertDockerImageUserFailure(t *testing.T, raw []byte, test dockerImageUserFailureTest) {
	t.Helper()
	var requests atomic.Int32
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, imageInspectDocument(test.expected, true))
		case 2:
			response.Header().Set(contentTypeHeader, dockerArchiveContentType)
			_, _ = response.Write(raw)
		case 3:
			if test.after != nil {
				test.after(response)

				return
			}
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, imageInspectDocument(test.expected, true))
		}
	}))
	resolved, err := client.ResolveImageUser(t.Context(), test.expected, test.specification)
	if resolved != "" || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ResolveImageUser() = %q, %v", resolved, err)
	}
}

func TestProbeArchiveImageSeparatesMissingAndDrift(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)

	t.Run("missing", func(t *testing.T) {
		t.Parallel()

		client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set(contentTypeHeader, jsonContentType)
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"message":"No such image"}`)
		}))
		probe, err := client.ProbeImage(context.Background(), fixture.expected)
		if err != nil || probe.State != application.ImageProbeMissing || probe.Image != emptyImage() {
			t.Fatalf("ProbeImage(missing archive) = %#v, %v", probe, err)
		}
	})

	t.Run("inspect changed during save", func(t *testing.T) {
		t.Parallel()

		var requests atomic.Int32
		client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			switch requests.Add(1) {
			case 1:
				response.Header().Set(contentTypeHeader, jsonContentType)
				_, _ = io.WriteString(response, archiveInspectDocument(fixture.expected, false))
			case 2:
				response.Header().Set(contentTypeHeader, dockerArchiveContentType)
				_, _ = response.Write(fixture.raw)
			case 3:
				changed := fixture.expected
				changed.ImageConfig = domain.Hash([]byte("changed config"))
				response.Header().Set(contentTypeHeader, jsonContentType)
				_, _ = io.WriteString(response, archiveInspectDocument(changed, false))
			}
		}))
		probe, err := client.ProbeImage(context.Background(), fixture.expected)
		if err == nil || probe != emptyImageProbe() {
			t.Fatalf("ProbeImage(changed archive) = %#v, %v", probe, err)
		}
	})
}

func TestProbeArchiveImageContainsLaterProofFailures(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	tests := []struct {
		name     string
		expected domain.ImageIdentity
		saveBody []byte
		after    func(http.ResponseWriter)
	}{
		{
			name:     "archive analysis",
			expected: fixture.expected,
			saveBody: []byte("not an archive"),
		},
		{
			name:     "post-save missing",
			expected: fixture.expected,
			saveBody: fixture.raw,
			after: func(response http.ResponseWriter) {
				response.Header().Set(contentTypeHeader, jsonContentType)
				response.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(response, `{"message":"No such image"}`)
			},
		},
		{
			name: "archive identity",
			expected: func() domain.ImageIdentity {
				value := fixture.expected
				value.ReferenceDigest = domain.Hash([]byte("different archive manifest"))

				return value
			}(),
			saveBody: fixture.raw,
		},
		{
			name: "platform manifest identity",
			expected: func() domain.ImageIdentity {
				value := fixture.expected
				value.PlatformManifest = domain.Hash([]byte("different platform manifest"))

				return value
			}(),
			saveBody: fixture.raw,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assertLaterArchiveProofFailure(t, test.expected, test.saveBody, test.after)
		})
	}
}

func assertLaterArchiveProofFailure(
	t *testing.T,
	expected domain.ImageIdentity,
	saveBody []byte,
	after func(http.ResponseWriter),
) {
	t.Helper()

	var requests atomic.Int32
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, archiveInspectDocument(expected, false))
		case 2:
			response.Header().Set(contentTypeHeader, dockerArchiveContentType)
			_, _ = response.Write(saveBody)
		case 3:
			if after != nil {
				after(response)

				return
			}
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, archiveInspectDocument(expected, false))
		}
	}))
	probe, err := client.ProbeImage(context.Background(), expected)
	if !errors.Is(err, ErrProtocol) || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(later proof failure) = %#v, %v", probe, err)
	}
}

func TestAnalyzeSavedArchiveRejectsContentLengthMismatch(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{ //nolint:exhaustruct // The archive analyzer only consumes these response fields.
			StatusCode:    http.StatusOK,
			Header:        http.Header{contentTypeHeader: []string{dockerArchiveContentType}},
			Body:          io.NopCloser(bytes.NewReader(fixture.raw)),
			ContentLength: int64(len(fixture.raw) + 1),
		}, nil
	}))
	client.version = testVersion()

	analysis, err := client.analyzeSavedArchive(
		context.Background(),
		fixture.expected.Reference,
		fixture.expected.Platform,
	)
	if err == nil || analysis.ArchiveSize != 0 {
		t.Fatalf("analyzeSavedArchive(content-length mismatch) = %#v, %v", analysis, err)
	}
}

func TestAnalyzeSavedArchiveAllowsLongRunningStreams(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	client := testClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		deadline, ok := request.Context().Deadline()
		remaining := time.Until(deadline)
		if !ok || remaining <= 0 || remaining > maximumDockerSaveProofDuration {
			t.Errorf("archive proof request deadline = %v, %t", deadline, ok)
		}

		return &http.Response{ //nolint:exhaustruct // The archive analyzer only consumes these response fields.
			StatusCode: http.StatusOK,
			Header:     http.Header{contentTypeHeader: []string{dockerArchiveContentType}},
			Body: &delayedReadCloser{
				delay:  20 * time.Millisecond,
				reader: bytes.NewReader(fixture.raw),
			},
			ContentLength: int64(len(fixture.raw)),
		}, nil
	}))
	client.httpClient.Timeout = time.Millisecond
	client.version = testVersion()

	analysis, err := client.analyzeSavedArchive(
		context.Background(),
		fixture.expected.Reference,
		fixture.expected.Platform,
	)
	if err != nil || analysis.ArchiveSize != int64(len(fixture.raw)) {
		t.Fatalf("analyzeSavedArchive(long-running stream) = %#v, %v", analysis, err)
	}
}

func TestAnalyzeSavedArchiveRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	tests := []struct {
		name          string
		status        int
		contentType   string
		contentLength int64
		body          []byte
	}{
		{name: testStatusCase, status: http.StatusInternalServerError,
			contentType: dockerArchiveContentType, contentLength: -1},
		{name: "content type", status: http.StatusOK, contentType: jsonContentType, contentLength: -1},
		{name: "zero length", status: http.StatusOK, contentType: dockerArchiveContentType, contentLength: 0},
		{name: testOversizedCase, status: http.StatusOK, contentType: dockerArchiveContentType,
			contentLength: maximumDockerSaveBytes + 1},
		{name: "malformed archive", status: http.StatusOK, contentType: dockerArchiveContentType,
			contentLength: -1, body: []byte("not a tar")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Header: http.Header{
					contentTypeHeader: []string{test.contentType},
				}, Body: io.NopCloser(bytes.NewReader(test.body)), ContentLength: test.contentLength}, nil
			}))
			client.version = testVersion()
			analysis, err := client.analyzeSavedArchive(
				context.Background(), fixture.expected.Reference, fixture.expected.Platform,
			)
			if !errors.Is(err, ErrProtocol) || analysis.ArchiveSize != 0 {
				t.Fatalf("analyzeSavedArchive() = %#v, %v", analysis, err)
			}
		})
	}
}

func TestAnalyzeSavedArchiveContainsTransportAndCancellation(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errImageTestTransport
	}))
	client.version = testVersion()
	if _, err := client.analyzeSavedArchive(
		context.Background(), fixture.expected.Reference, fixture.expected.Platform,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("analyzeSavedArchive(transport) error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	client = testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		cancel()

		return &http.Response{ //nolint:exhaustruct // The archive analyzer only consumes these response fields.
			StatusCode:    http.StatusOK,
			Header:        http.Header{contentTypeHeader: []string{dockerArchiveContentType}},
			Body:          io.NopCloser(strings.NewReader("not an archive")),
			ContentLength: -1,
		}, nil
	}))
	client.version = testVersion()
	if _, err := client.analyzeSavedArchive(
		ctx, fixture.expected.Reference, fixture.expected.Platform,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("analyzeSavedArchive(cancelled analysis) error = %v", err)
	}
}

func TestArchiveProbeContainsTransportAndInspectProtocolFailures(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: "bad missing", status: http.StatusNotFound, contentType: jsonContentType, body: `{}`},
		{name: "status", status: http.StatusInternalServerError, contentType: jsonContentType, body: `{}`},
		{name: "content type", status: http.StatusOK, contentType: plainTextContentType, body: `{}`},
		{name: "malformed", status: http.StatusOK, contentType: jsonContentType, body: `{`},
		{name: "invalid inspect", status: http.StatusOK, contentType: jsonContentType, body: `{}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set(contentTypeHeader, test.contentType)
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))
			probe, err := client.ProbeImage(context.Background(), fixture.expected)
			if !errors.Is(err, ErrProtocol) || probe != emptyImageProbe() {
				t.Fatalf("ProbeImage() = %#v, %v", probe, err)
			}
		})
	}

	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errImageTestTransport }))
	client.version = testVersion()
	probe, err := client.ProbeImage(context.Background(), fixture.expected)
	if !errors.Is(err, ErrUnavailable) || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(transport) = %#v, %v", probe, err)
	}
}

func newArchiveProbeFixture(t *testing.T) archiveProbeFixture {
	t.Helper()

	return newArchiveProbeFixtureKind(t, false)
}

func newArchiveProbeFixtureWithNestedOCI(t *testing.T) archiveProbeFixture {
	t.Helper()

	return newArchiveProbeFixtureKind(t, true)
}

func newArchiveProbeFixtureKind(t *testing.T, nestedOCI bool) archiveProbeFixture {
	t.Helper()

	var layerOutput bytes.Buffer
	layerWriter := tar.NewWriter(&layerOutput)
	writeArchiveProbeMember(
		t,
		layerWriter,
		"etc/passwd",
		[]byte("root:x:0:0:root:/root:/bin/sh\nservice:x:1001:1002::/srv/service:/bin/sh\n"),
	)
	if err := layerWriter.Close(); err != nil {
		t.Fatalf("close archive layer fixture: %v", err)
	}
	layer := layerOutput.Bytes()
	config := fmt.Appendf(nil, `{"architecture":"amd64","os":"linux",`+
		`"rootfs":{"type":"layers","diff_ids":[%q]},`+
		`"config":{"ArgsEscaped":true,"Entrypoint":["/init"],"Cmd":["serve"]}}`,
		domain.Hash(layer).String())
	manifest := []byte(`[{"Config":"config.json","RepoTags":["` + testArchiveReference +
		`"],"Layers":["layer.tar"]}]`)
	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	writeArchiveProbeMember(t, writer, "manifest.json", manifest)
	writeArchiveProbeMember(t, writer, "config.json", config)
	writeArchiveProbeMember(t, writer, "layer.tar", layer)
	if nestedOCI {
		writeNestedArchiveProbeOCI(t, writer, config, layer)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close archive fixture: %v", err)
	}

	path := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(path, output.Bytes(), 0o600); err != nil {
		t.Fatalf("write archive fixture: %v", err)
	}
	source, err := imagearchive.ParseSource("docker-archive:" + path + ":" + testArchiveReference)
	if err != nil {
		t.Fatalf("ParseSource() error = %v", err)
	}
	analysis, err := imagearchive.Analyze(context.Background(), source)
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	expected := analysis.Identity

	return archiveProbeFixture{raw: bytes.Clone(output.Bytes()), expected: expected}
}

func writeNestedArchiveProbeOCI(t *testing.T, writer *tar.Writer, config, layer []byte) {
	t.Helper()

	imageManifest := fmt.Appendf(nil,
		`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
			`"config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},`+
			`"layers":[{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":%q,"size":%d}]}`,
		domain.Hash(config).String(), len(config), domain.Hash(layer).String(), len(layer),
	)
	platformIndex := fmt.Appendf(nil,
		`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.manifest.v1+json",`+
			`"digest":%q,"size":%d,"platform":{"os":"linux","architecture":"amd64"}}]}`,
		domain.Hash(imageManifest).String(), len(imageManifest),
	)
	topIndex := fmt.Appendf(nil,
		`{"schemaVersion":2,"manifests":[{"mediaType":"application/vnd.oci.image.index.v1+json",`+
			`"digest":%q,"size":%d}]}`,
		domain.Hash(platformIndex).String(), len(platformIndex),
	)
	writeArchiveProbeMember(t, writer, "index.json", topIndex)
	for _, blob := range [][]byte{platformIndex, imageManifest, config, layer} {
		writeArchiveProbeMember(t, writer, archiveProbeBlobName(blob), blob)
	}
}

func archiveProbeBlobName(value []byte) string {
	return "blobs/sha256/" + strings.TrimPrefix(domain.Hash(value).String(), "sha256:")
}

func writeArchiveProbeMember(t *testing.T, writer *tar.Writer, name string, body []byte) {
	t.Helper()

	header := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
	if err := writer.WriteHeader(header); err != nil {
		t.Fatalf("write archive header: %v", err)
	}
	if _, err := writer.Write(body); err != nil {
		t.Fatalf("write archive member: %v", err)
	}
}

func assertArchiveInspectRequest(t *testing.T, request *http.Request, expected domain.ImageIdentity) {
	t.Helper()

	wantPath := "/v1.54/images/" + testArchiveReference + "/json"
	assertArchiveRequest(t, request, expected, wantPath)
}

func assertArchiveSaveRequest(t *testing.T, request *http.Request, expected domain.ImageIdentity) {
	t.Helper()

	wantPath := "/v1.54/images/" + testArchiveReference + "/get"
	assertArchiveRequest(t, request, expected, wantPath)
	if request.Header.Get("Accept") != dockerArchiveContentType {
		t.Fatalf("archive save Accept = %q", request.Header.Get("Accept"))
	}
}

func assertArchiveRequest(
	t *testing.T,
	request *http.Request,
	expected domain.ImageIdentity,
	wantPath string,
) {
	t.Helper()

	if request.Method != http.MethodGet || request.URL.Path != wantPath ||
		request.URL.Query().Get(imagePullPlatformQuery) != imagePlatform(expected.Platform) {
		t.Fatalf("archive request = %s %s?%s", request.Method, request.URL.Path, request.URL.RawQuery)
	}
}

func archiveInspectDocument(expected domain.ImageIdentity, containerdStore bool) string {
	document := fmt.Sprintf(
		`{"Id":%q,"RepoTags":[%q],"RepoDigests":[],"Config":{},`+
			`"Architecture":%q,"Variant":%q,"Os":%q,"Size":0}`,
		expected.ImageConfig.String(),
		expected.Reference,
		expected.Platform.Architecture,
		expected.Platform.Variant,
		expected.Platform.OS,
	)
	if !containerdStore {
		return document
	}
	runtimeManifest := domain.Hash([]byte("runtime-specific imported manifest"))

	return strings.Replace(document,
		`"Id":`+fmt.Sprintf("%q", expected.ImageConfig.String()),
		`"Id":`+fmt.Sprintf("%q", runtimeManifest.String())+`,"Descriptor":{`+
			fmt.Sprintf(`"mediaType":%q,"digest":%q,"size":123}`, ociManifestMediaType,
				runtimeManifest.String()),
		1,
	)
}

func TestArchiveProbeHelpersRejectInvalidValues(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	invalid := fixture.expected
	invalid.Reference = strings.Replace(invalid.Reference, ":1", "@bad", 1)
	probe, err := connectedTestClient(t, nil).ProbeImage(context.Background(), invalid)
	if err == nil || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(invalid archive) = %#v, %v", probe, err)
	}

	unknownOrigin := fixture.expected
	unknownOrigin.Origin = domain.ImageOrigin(255)
	probe, err = connectedTestClient(t, nil).ProbeImage(context.Background(), unknownOrigin)
	if !errors.Is(err, ErrUnsupportedWorkload) || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(unknown origin) = %#v, %v", probe, err)
	}
	unknownOrigin.Origin = domain.ImageOriginUnknown
	probe, err = connectedTestClient(t, nil).ProbeImage(context.Background(), unknownOrigin)
	if !errors.Is(err, ErrUnsupportedWorkload) || probe != emptyImageProbe() {
		t.Fatalf("ProbeImage(zero origin) = %#v, %v", probe, err)
	}
	if validDockerImage(testVersion(), unknownOrigin) {
		t.Fatal("validDockerImage(unknown origin) succeeded")
	}
	if !validDockerImage(testVersion(), fixture.expected) {
		t.Fatal("validDockerImage(archive) failed")
	}
}

func TestArchiveImageHelpersContainProtocolBoundaries(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	testArchiveRequestConstructionFailures(t, fixture)
	testArchiveInspectValidationFailures(t, fixture)
}

func testArchiveRequestConstructionFailures(t *testing.T, fixture archiveProbeFixture) {
	t.Helper()

	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errImageTestTransport
	}))
	client.version = Version{}
	if _, state, err := client.inspectArchiveImage(
		context.Background(), fixture.expected,
	); !errors.Is(err, ErrProtocol) || state != application.ImageProbeUnknown {
		t.Fatalf("inspectArchiveImage(invalid version) = %v, %v", state, err)
	}
	response, err := client.saveArchiveImage(
		context.Background(), fixture.expected.Reference, fixture.expected.Platform,
	)
	if response != nil {
		closeResponse(response)
	}
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("saveArchiveImage(invalid version) error = %v", err)
	}

	client.version = testVersion()
	client.baseURL.Host = "invalid\nvalue"
	response, err = client.saveArchiveImage(
		context.Background(), fixture.expected.Reference, fixture.expected.Platform,
	)
	if response != nil {
		closeResponse(response)
	}
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("saveArchiveImage(invalid URL) error = %v", err)
	}

	client = testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errImageTestTransport
	}))
	client.version = testVersion()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	response, err = client.saveArchiveImage(cancelled, fixture.expected.Reference, fixture.expected.Platform)
	if response != nil {
		closeResponse(response)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("saveArchiveImage(cancelled) error = %v", err)
	}
}

func testArchiveInspectValidationFailures(t *testing.T, fixture archiveProbeFixture) {
	t.Helper()

	payload := decodeArchiveInspectDocument(t, archiveInspectDocument(fixture.expected, true))
	payload.Descriptor.Size = 0
	if _, valid := archiveImageInspectSnapshot(payload, fixture.expected); valid {
		t.Fatal("archiveImageInspectSnapshot(invalid descriptor) succeeded")
	}
	payload = decodeArchiveInspectDocument(t, archiveInspectDocument(fixture.expected, false))
	payload.Architecture = dockerArchitectureARM64
	if validArchiveImageInspect(payload, fixture.expected) {
		t.Fatal("validArchiveImageInspect(platform drift) succeeded")
	}
	payload.Architecture = fixture.expected.Platform.Architecture
	payload.RepoTags = nil
	if validArchiveImageInspect(payload, fixture.expected) {
		t.Fatal("validArchiveImageInspect(missing tag) succeeded")
	}
}

func decodeArchiveInspectDocument(t *testing.T, document string) imagetypes.InspectResponse {
	t.Helper()

	var payload imagetypes.InspectResponse
	if !decodeStrictJSON(strings.NewReader(document), &payload) {
		t.Fatal("decodeStrictJSON(archive inspect fixture) failed")
	}

	return payload
}
