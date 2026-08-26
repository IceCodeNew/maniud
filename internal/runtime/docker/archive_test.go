package docker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/backup"
)

//nolint:cyclop,funlen // The test handler covers the fixed archive route matrix.
func TestDockerWorkloadArchiveTransfersValidatedPath(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	archivePath := "/var/lib/maniud"
	archive := []byte("validated tar stream")
	stat := dockerArchiveStatHeader(t, archivePath, int64(len(archive)))
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case testContainerListPath:
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, `[{"Id":"`+testContainerID+`"}]`)
		case "/v1.55/containers/" + testContainerID + "/json":
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, validContainerDocument(
				t, workloadOwnershipLabels(workload, testTransaction), runningContainerState(),
			))
		case "/v1.55/containers/" + testContainerID + "/archive":
			if request.URL.Query().Get(archivePathQuery) != archivePath {
				t.Errorf("archive path = %q", request.URL.Query().Get(archivePathQuery))
			}
			switch request.Method {
			case http.MethodHead:
				response.Header().Set("X-Docker-Container-Path-Stat", stat)
				response.WriteHeader(http.StatusOK)
			case http.MethodGet:
				response.Header().Set(contentTypeHeader, dockerArchiveContentType)
				response.Header().Set("X-Docker-Container-Path-Stat", stat)
				_, _ = response.Write(archive)
			case http.MethodPut:
				if request.URL.Query().Get("copyUIDGID") != dockerQueryTrue ||
					request.URL.Query().Get("noOverwriteDirNonDir") != dockerQueryTrue {
					t.Errorf("archive restore query = %q", request.URL.RawQuery)
				}
				body, err := io.ReadAll(request.Body)
				if err != nil || !bytes.Equal(body, archive) {
					t.Errorf("archive upload = %q, %v", body, err)
				}
				response.WriteHeader(http.StatusOK)
			}
		default:
			http.NotFound(response, request)
		}
	}))

	probed, err := client.ProbeWorkloadArchivePath(context.Background(), workload, testTransaction, archivePath)
	if err != nil || probed.Name != path.Base(archivePath) || probed.Size != int64(len(archive)) {
		t.Fatalf("ProbeWorkloadArchivePath() = %#v, %v", probed, err)
	}

	var destination bytes.Buffer
	got, err := client.GetWorkloadArchive(
		context.Background(), workload, testTransaction, archivePath, &destination, 1024,
	)
	if err != nil || !bytes.Equal(destination.Bytes(), archive) || got != probed {
		t.Fatalf("GetWorkloadArchive() = %#v, %q, %v", got, destination.Bytes(), err)
	}

	if err := client.PutWorkloadArchive(
		context.Background(), workload, testTransaction, archivePath, bytes.NewReader(archive),
	); err != nil {
		t.Fatalf("PutWorkloadArchive() error = %v", err)
	}
}

func TestDockerWorkloadArchiveRejectsInvalidEvidenceAndLimits(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case testContainerListPath:
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, `[{"Id":"`+testContainerID+`"}]`)
		case "/v1.55/containers/" + testContainerID + "/json":
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, validContainerDocument(
				t, workloadOwnershipLabels(workload, testTransaction), runningContainerState(),
			))
		case "/v1.55/containers/" + testContainerID + "/archive":
			response.Header().Set(contentTypeHeader, dockerArchiveContentType)
			response.Header().Set("X-Docker-Container-Path-Stat", dockerArchiveStatHeader(t, "/data", 5))
			_, _ = response.Write([]byte("oversized"))
		default:
			http.NotFound(response, request)
		}
	}))

	var destination bytes.Buffer
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", &destination, 4,
	); !errors.Is(err, backup.ErrArchiveLimit) {
		t.Fatalf("GetWorkloadArchive(limit) error = %v", err)
	}
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, testTransaction, "/data/../data", &destination, 4,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("GetWorkloadArchive(path) error = %v", err)
	}
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", nil, 4,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("GetWorkloadArchive(destination) error = %v", err)
	}
}

func TestDockerArchiveHelpersRejectMalformedValues(t *testing.T) {
	t.Parallel()

	if _, err := decodeDockerArchivePathStat("not-base64"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeDockerArchivePathStat(malformed) = %v", err)
	}
	invalidStat := base64.StdEncoding.EncodeToString([]byte(`{"name":"","size":-1}`))
	if _, err := decodeDockerArchivePathStat(invalidStat); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeDockerArchivePathStat(incomplete) = %v", err)
	}
	if err := copyDockerArchive(nil, io.Discard, 1); !errors.Is(err, ErrProtocol) {
		t.Fatalf("copyDockerArchive(nil) = %v", err)
	}
	response := &http.Response{Body: io.NopCloser(strings.NewReader("x"))}
	if err := copyDockerArchive(response, io.Discard, 0); !errors.Is(err, ErrProtocol) {
		t.Fatalf("copyDockerArchive(zero limit) = %v", err)
	}
}

//nolint:cyclop,funlen // The table covers independent malformed archive boundaries.
func TestDockerArchiveHelperErrorMatrix(t *testing.T) {
	t.Parallel()

	if _, err := decodeDockerArchivePathStat(""); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeDockerArchivePathStat(empty) = %v", err)
	}
	unknownStat := base64.StdEncoding.EncodeToString([]byte(`{"unknown":true}`))
	if _, err := decodeDockerArchivePathStat(unknownStat); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeDockerArchivePathStat(unknown) = %v", err)
	}
	if err := decodeDockerArchivePutResponse(nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeDockerArchivePutResponse(nil) = %v", err)
	}
	badStatus := &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(""))}
	if err := decodeDockerArchivePutResponse(badStatus); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeDockerArchivePutResponse(status) = %v", err)
	}
	nonEmpty := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("body"))}
	if err := decodeDockerArchivePutResponse(nonEmpty); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeDockerArchivePutResponse(body) = %v", err)
	}
	closeFailure := &http.Response{StatusCode: http.StatusOK, Body: &dockerArchiveTestBody{closeErr: io.ErrClosedPipe}}
	if err := decodeDockerArchivePutResponse(closeFailure); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("decodeDockerArchivePutResponse(close) = %v", err)
	}
	readFailure := &http.Response{StatusCode: http.StatusOK, Body: &dockerArchiveTestBody{readErr: io.ErrUnexpectedEOF}}
	if err := decodeDockerArchivePutResponse(readFailure); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("decodeDockerArchivePutResponse(read) = %v", err)
	}

	if err := copyDockerArchive(&http.Response{
		ContentLength: 2, Body: io.NopCloser(strings.NewReader("xx")),
	}, io.Discard, 1); !errors.Is(err, backup.ErrArchiveLimit) {
		t.Fatalf("copyDockerArchive(content length) = %v", err)
	}
	if err := copyDockerArchive(&http.Response{
		ContentLength: -2, Body: io.NopCloser(strings.NewReader("x")),
	}, io.Discard, 1); !errors.Is(err, ErrProtocol) {
		t.Fatalf("copyDockerArchive(invalid content length) = %v", err)
	}
	if err := copyDockerArchive(&http.Response{
		ContentLength: -1, Body: &dockerArchiveTestBody{readErr: io.ErrUnexpectedEOF},
	}, io.Discard, 10); err == nil {
		t.Fatal("copyDockerArchive(read) returned nil")
	}
	if err := copyDockerArchive(&http.Response{
		ContentLength: -1, Body: &dockerArchiveTestBody{data: []byte("x"), closeErr: io.ErrClosedPipe},
	}, io.Discard, 10); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyDockerArchive(close) = %v", err)
	}
	if err := copyDockerArchive(&http.Response{
		ContentLength: -1, Body: io.NopCloser(strings.NewReader("xx")),
	}, io.Discard, 1); !errors.Is(err, backup.ErrArchiveLimit) {
		t.Fatalf("copyDockerArchive(written limit) = %v", err)
	}

	if !validArchivePath("/data") || validArchivePath("data") || validArchivePath("/data/../data") ||
		validArchivePath("/data\\child") || validArchivePath("/data\x00") {
		t.Fatal("validArchivePath() accepted an invalid path")
	}
	if !validOptionalArchiveText("") || validOptionalArchiveText("\x00") || !validIdentityEncoding("") ||
		!validIdentityEncoding("identity") || validIdentityEncoding("gzip") {
		t.Fatal("archive text or encoding validation changed")
	}

	for _, test := range []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, application.ErrArchiveConflict},
		{http.StatusForbidden, application.ErrArchiveConflict},
		{http.StatusNotFound, application.ErrArchivePathMissing},
		{http.StatusInternalServerError, ErrUnavailable},
	} {
		response := &http.Response{
			StatusCode: test.status,
			Header:     http.Header{contentTypeHeader: {jsonContentType}},
			Body:       io.NopCloser(strings.NewReader(`{"message":"archive failed"}`)),
		}
		if err := dockerArchiveResponseError(response); !errors.Is(err, test.want) {
			t.Errorf("dockerArchiveResponseError(%d) = %v", test.status, err)
		}
	}
}

func TestDockerWorkloadArchiveHeadUsesStatusWithoutBody(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case testContainerListPath:
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, `[{"Id":"`+testContainerID+`"}]`)
		case "/v1.55/containers/" + testContainerID + "/json":
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, validContainerDocument(
				t, workloadOwnershipLabels(workload, testTransaction), runningContainerState(),
			))
		case "/v1.55/containers/" + testContainerID + "/archive":
			response.Header().Set(contentTypeHeader, jsonContentType)
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"message":"path missing"}`)
		default:
			http.NotFound(response, request)
		}
	}))

	_, err := client.ProbeWorkloadArchivePath(context.Background(), workload, testTransaction, "/data")
	if !errors.Is(err, application.ErrArchivePathMissing) {
		t.Fatalf("ProbeWorkloadArchivePath() error = %v", err)
	}
}

type dockerArchiveTestBody struct {
	data     []byte
	readErr  error
	closeErr error
}

func (body *dockerArchiveTestBody) Read(value []byte) (int, error) {
	if len(body.data) != 0 {
		count := copy(value, body.data)
		body.data = body.data[count:]

		return count, nil
	}

	if body.readErr != nil {
		return 0, body.readErr
	}

	return 0, io.EOF
}

func (body *dockerArchiveTestBody) Close() error { return body.closeErr }

func dockerArchiveStatHeader(t *testing.T, archivePath string, size int64) string {
	t.Helper()

	raw, err := json.Marshal(struct {
		Name       string      `json:"name"`
		Size       int64       `json:"size"`
		Mode       os.FileMode `json:"mode"`
		ModTime    time.Time   `json:"mtime"`
		LinkTarget string      `json:"linkTarget"` //nolint:tagliatelle // Docker archive API wire field.
	}{
		Name: path.Base(archivePath), Size: size, Mode: os.ModeDir | 0o755,
		ModTime: time.Unix(100, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	return base64.StdEncoding.EncodeToString(raw)
}
