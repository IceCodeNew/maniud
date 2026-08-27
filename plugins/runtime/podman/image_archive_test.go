package podman

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/imagerootfs"
)

const testPodmanImageUser = "service"

type podmanCancelOnCloseBody struct {
	io.Reader

	cancel context.CancelFunc
}

func (body podmanCancelOnCloseBody) Close() error {
	body.cancel()

	return nil
}

type podmanImageUserFailureTest struct {
	name          string
	specification string
	want          error
	export        func(http.ResponseWriter)
	after         func(http.ResponseWriter)
}

func TestResolveImageUserReadsImmutablePodmanExport(t *testing.T) {
	t.Parallel()

	raw, expected := podmanUserArchiveFixture(t)
	var requests atomic.Int32
	handlers := []func(http.ResponseWriter, *http.Request){
		func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet ||
				request.URL.Path != libpodPrefix+"/images/"+expected.Reference+"/json" {
				t.Fatalf("probe request = %s %s", request.Method, request.URL.String())
			}
			writePodmanJSON(writer, podmanUserImageDocument(expected))
		},
		func(writer http.ResponseWriter, request *http.Request) {
			assertPodmanUserArchiveRequest(t, request, expected)
			writer.Header().Set(podmanContentType, podmanArchiveContentType)
			_, _ = writer.Write(raw)
		},
		func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet ||
				request.URL.Path != libpodPrefix+"/images/"+expected.Reference+"/json" {
				t.Fatalf("probe request = %s %s", request.Method, request.URL.String())
			}
			writePodmanJSON(writer, podmanUserImageDocument(expected))
		},
	}
	client := connectedPodmanImageClient(t, func(writer http.ResponseWriter, request *http.Request) {
		requestIndex := int(requests.Add(1)) - 1
		if requestIndex >= len(handlers) {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL.String())
		}
		handlers[requestIndex](writer, request)
	})

	resolved, err := client.ResolveImageUser(t.Context(), expected, testPodmanImageUser)
	if err != nil || resolved != "1001:1002" || requests.Load() != 3 {
		t.Fatalf("ResolveImageUser() = %q, %v, requests %d", resolved, err, requests.Load())
	}
	if _, err = (*Client)(nil).ResolveImageUser(t.Context(), expected, testPodmanImageUser); !errors.Is(
		err,
		ErrUnsupportedWorkload,
	) {
		t.Fatalf("ResolveImageUser(nil client) = %v", err)
	}
}

func assertPodmanUserArchiveRequest(t *testing.T, request *http.Request, expected domain.ImageIdentity) {
	t.Helper()

	wantID := strings.TrimPrefix(expected.ImageConfig.String(), "sha256:")
	if request.Method != http.MethodGet ||
		request.URL.Path != libpodPrefix+"/images/"+wantID+"/get" ||
		request.URL.Query().Get("format") != podmanDockerArchiveFormat ||
		request.Header.Get("Accept") != podmanArchiveContentType {
		t.Fatalf(
			"archive request = %s %s, Accept %q",
			request.Method,
			request.URL.String(),
			request.Header.Get("Accept"),
		)
	}
}

func TestResolveImageUserRejectsExportAndPostProbeFailures(t *testing.T) {
	t.Parallel()

	raw, expected := podmanUserArchiveFixture(t)
	for _, test := range podmanImageUserFailureTests(raw) {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			assertPodmanImageUserFailure(t, expected, test)
		})
	}
}

func podmanImageUserFailureTests(raw []byte) []podmanImageUserFailureTest {
	return []podmanImageUserFailureTest{
		{
			name:          "export request",
			specification: testPodmanImageUser,
			want:          ErrProtocol,
			export: func(writer http.ResponseWriter) {
				writer.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name:          "export response",
			specification: testPodmanImageUser,
			want:          ErrProtocol,
			export: func(writer http.ResponseWriter) {
				writer.Header().Set(podmanContentType, podmanJSONType)
				_, _ = io.WriteString(writer, `{}`)
			},
		},
		{
			name:          "archive analysis",
			specification: testPodmanImageUser,
			want:          ErrProtocol,
			export: func(writer http.ResponseWriter) {
				writer.Header().Set(podmanContentType, podmanArchiveContentType)
				_, _ = io.WriteString(writer, "not an archive")
			},
		},
		{
			name:          "post-probe failure",
			specification: testPodmanImageUser,
			want:          ErrProtocol,
			export: func(writer http.ResponseWriter) {
				writer.Header().Set(podmanContentType, podmanArchiveContentType)
				_, _ = writer.Write(raw)
			},
			after: func(writer http.ResponseWriter) {
				writer.WriteHeader(http.StatusInternalServerError)
			},
		},
		{
			name:          "post-probe missing",
			specification: testPodmanImageUser,
			want:          ErrProtocol,
			export: func(writer http.ResponseWriter) {
				writer.Header().Set(podmanContentType, podmanArchiveContentType)
				_, _ = writer.Write(raw)
			},
			after: writeMissingPodmanImage,
		},
		{
			name:          "user resolution",
			specification: "missing-user",
			want:          imagerootfs.ErrUnknownUser,
			export: func(writer http.ResponseWriter) {
				writer.Header().Set(podmanContentType, podmanArchiveContentType)
				_, _ = writer.Write(raw)
			},
		},
	}
}

func TestResolveImageUserRejectsMissingInitialImage(t *testing.T) {
	t.Parallel()

	_, expected := podmanUserArchiveFixture(t)
	client := connectedPodmanImageClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writeMissingPodmanImage(writer)
	})
	resolved, err := client.ResolveImageUser(t.Context(), expected, testPodmanImageUser)
	if resolved != "" || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ResolveImageUser(missing) = %q, %v", resolved, err)
	}
}

func writeMissingPodmanImage(writer http.ResponseWriter) {
	writer.Header().Set(podmanContentType, podmanJSONType)
	writer.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(writer, `{"cause":"image unknown","message":"image unknown","response":404}`)
}

func assertPodmanImageUserFailure(
	t *testing.T,
	expected domain.ImageIdentity,
	test podmanImageUserFailureTest,
) {
	t.Helper()
	var requests atomic.Int32
	client := connectedPodmanImageClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		switch requests.Add(1) {
		case 1:
			writePodmanJSON(writer, podmanUserImageDocument(expected))
		case 2:
			test.export(writer)
		case 3:
			if test.after != nil {
				test.after(writer)

				return
			}
			writePodmanJSON(writer, podmanUserImageDocument(expected))
		}
	})
	resolved, err := client.ResolveImageUser(t.Context(), expected, test.specification)
	if resolved != "" || !errors.Is(err, test.want) {
		t.Fatalf("ResolveImageUser() = %q, %v, want %v", resolved, err, test.want)
	}
}

func TestResolveImageUserContainsExportTransportFailure(t *testing.T) {
	t.Parallel()

	_, expected := podmanUserArchiveFixture(t)
	client := connectedPodmanImageClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writePodmanJSON(writer, podmanUserImageDocument(expected))
	})
	transport := client.httpClient.Transport
	client.httpClient.Transport = podmanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/get") {
			return nil, errPodmanImageTest
		}

		return transport.RoundTrip(request)
	})
	resolved, err := client.ResolveImageUser(t.Context(), expected, testPodmanImageUser)
	if resolved != "" || !errors.Is(err, ErrUnavailable) || errors.Is(err, ErrProtocol) {
		t.Fatalf("ResolveImageUser() = %q, %v", resolved, err)
	}
}

func TestResolveSavedImageUserPreservesArchiveFailures(t *testing.T) {
	t.Parallel()

	raw, expected := podmanUserArchiveFixture(t)
	drifted := expected
	drifted.ImageConfig = domain.Hash([]byte("another image config"))
	tests := []struct {
		name     string
		expected domain.ImageIdentity
		body     func() io.ReadCloser
		want     error
	}{
		{
			name: "archive read", expected: expected, want: io.ErrUnexpectedEOF,
			body: func() io.ReadCloser {
				return &podmanArchiveTestBody{data: nil, readErr: io.ErrUnexpectedEOF, closeErr: nil}
			},
		},
		{
			name: "archive close", expected: expected, want: errPodmanImageTest,
			body: func() io.ReadCloser {
				return &podmanArchiveTestBody{data: bytes.Clone(raw), readErr: nil, closeErr: errPodmanImageTest}
			},
		},
		{
			name: "archive identity", expected: drifted, want: ErrProtocol,
			body: func() io.ReadCloser {
				return &podmanArchiveTestBody{data: bytes.Clone(raw), readErr: nil, closeErr: nil}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedPodmanImageClient(t, func(http.ResponseWriter, *http.Request) {})
			client.httpClient.Transport = podmanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				return &http.Response{ //nolint:exhaustruct // The archive proof reads only these response fields.
					StatusCode:    http.StatusOK,
					Header:        http.Header{podmanContentType: {podmanArchiveContentType}},
					Body:          test.body(),
					ContentLength: int64(len(raw)),
					Request:       request,
				}, nil
			})
			resolved, err := client.resolveSavedImageUser(t.Context(), test.expected, testPodmanImageUser)
			if resolved != "" || !errors.Is(err, test.want) {
				t.Fatalf("resolveSavedImageUser() = %q, %v, want %v", resolved, err, test.want)
			}
		})
	}
}

func TestResolveImageUserPreservesProbeCancellation(t *testing.T) {
	t.Parallel()

	raw, expected := podmanUserArchiveFixture(t)
	client := connectedPodmanImageClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writePodmanJSON(writer, podmanUserImageDocument(expected))
	})

	cancelled, cancelBefore := context.WithCancel(t.Context())
	cancelBefore()
	if _, err := client.ResolveImageUser(cancelled, expected, testPodmanImageUser); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveImageUser(initial cancellation) = %v", err)
	}

	ctx, cancelAfter := context.WithCancel(t.Context())
	transport := client.httpClient.Transport
	client.httpClient.Transport = podmanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/get") {
			return &http.Response{ //nolint:exhaustruct // The archive proof reads only these response fields.
				StatusCode: http.StatusOK,
				Header:     http.Header{podmanContentType: {podmanArchiveContentType}},
				Body: podmanCancelOnCloseBody{
					Reader: bytes.NewReader(raw), cancel: cancelAfter,
				},
				ContentLength: int64(len(raw)),
				Request:       request,
			}, nil
		}

		return transport.RoundTrip(request)
	})
	if _, err := client.ResolveImageUser(ctx, expected, testPodmanImageUser); !errors.Is(err, context.Canceled) {
		t.Fatalf("ResolveImageUser(post-probe cancellation) = %v", err)
	}
}

func TestValidPodmanSavedArchiveRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	valid := func() *http.Response {
		return &http.Response{ //nolint:exhaustruct // This helper tests only archive response validation.
			StatusCode: http.StatusOK,
			Header: http.Header{
				podmanContentType: []string{podmanArchiveContentType + "; charset=binary"},
			},
			Body: io.NopCloser(strings.NewReader("archive")), ContentLength: -1,
		}
	}
	response := valid()
	if !validPodmanSavedArchive(response) {
		t.Fatal("validPodmanSavedArchive(valid) rejected")
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*http.Response){
		func(response *http.Response) { response.Body = nil },
		func(response *http.Response) { response.StatusCode = http.StatusCreated },
		func(response *http.Response) { response.ContentLength = -2 },
		func(response *http.Response) { response.ContentLength = 0 },
		func(response *http.Response) { response.ContentLength = maximumPodmanSaveBytes + 1 },
		func(response *http.Response) { response.Header.Set(podmanContentType, "invalid media type;") },
		func(response *http.Response) { response.Header.Set(podmanContentType, "application/json") },
	} {
		response = valid()
		body := response.Body
		mutate(response)
		if validPodmanSavedArchive(response) {
			t.Fatalf("validPodmanSavedArchive(%#v) accepted", response)
		}
		if err := body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if validPodmanSavedArchive(nil) {
		t.Fatal("validPodmanSavedArchive(nil) accepted")
	}
}

func podmanUserArchiveFixture(t *testing.T) ([]byte, domain.ImageIdentity) {
	t.Helper()

	layer := podmanTar(t, map[string][]byte{
		"etc/passwd": []byte("root:x:0:0:root:/root:/bin/sh\nservice:x:1001:1002::/srv/service:/bin/sh\n"),
	})
	config := fmt.Appendf(nil, `{"architecture":"amd64","os":"linux",`+
		`"rootfs":{"type":"layers","diff_ids":[%q]},"config":{}}`, domain.Hash(layer).String())
	manifest := []byte(`[{"Config":"config.json","RepoTags":["registry.example/team/app:1"],` +
		`"Layers":["layer.tar"]}]`)
	raw := podmanTar(t, map[string][]byte{
		"manifest.json": manifest,
		"config.json":   config,
		"layer.tar":     layer,
	})
	analysis, err := imagearchive.AnalyzeStream(context.Background(), bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("AnalyzeStream(user archive) = %v", err)
	}
	expected := analysis.Identity
	expected.Origin = domain.ImageOriginRegistry
	expected.ReferenceDigest = analysis.ManifestDigest
	expected.Reference = "registry.example/team/app@" + expected.ReferenceDigest.String()

	return raw, expected
}

func podmanTar(t *testing.T, members map[string][]byte) []byte {
	t.Helper()

	var output bytes.Buffer
	writer := tar.NewWriter(&output)
	for _, name := range []string{"manifest.json", "config.json", "layer.tar", "etc/passwd"} {
		body, found := members[name]
		if !found {
			continue
		}
		header := &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}
		if err := writer.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	return output.Bytes()
}

func podmanUserImageDocument(expected domain.ImageIdentity) string {
	return fmt.Sprintf(
		`{"Id":%q,"Digest":%q,"RepoDigests":[%q],"RepoTags":[],`+
			`"Os":%q,"Architecture":%q,"Size":1,"Config":{}}`,
		expected.ImageConfig.String(),
		expected.PlatformManifest.String(),
		expected.Reference,
		expected.Platform.OS,
		expected.Platform.Architecture,
	)
}
