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
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/backup"
)

//nolint:cyclop // The test handler covers the fixed archive route matrix.
func TestPodmanWorkloadArchiveTransfersValidatedPath(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	archivePath := "/var/lib/maniud"
	archive := []byte("validated tar stream")
	stat := podmanArchiveStatHeader(t, archivePath, int64(len(archive)))
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerRunning,
	}
	client := connectedPodmanWorkloadClient(t, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/archive") {
			if request.URL.Query().Get(archivePathQuery) != archivePath {
				t.Errorf("archive path = %q", request.URL.Query().Get(archivePathQuery))
			}
			switch request.Method {
			case http.MethodHead:
				response.Header().Set("X-Docker-Container-Path-Stat", stat)
				response.WriteHeader(http.StatusOK)
			case http.MethodGet:
				response.Header().Set(podmanContentType, podmanArchiveContentType)
				response.Header().Set("X-Docker-Container-Path-Stat", stat)
				_, _ = response.Write(archive)
			case http.MethodPut:
				if request.URL.Query().Get("copyUIDGID") != podmanQueryTrue ||
					request.URL.Query().Get("noOverwriteDirNonDir") != podmanQueryTrue {
					t.Errorf("archive restore query = %q", request.URL.RawQuery)
				}
				body, err := io.ReadAll(request.Body)
				if err != nil || !bytes.Equal(body, archive) {
					t.Errorf("archive upload = %q, %v", body, err)
				}
				response.WriteHeader(http.StatusOK)
			}

			return
		}
		state.handler(response, request)
	})

	probed, err := client.ProbeWorkloadArchivePath(context.Background(), workload, podmanTestTransaction, archivePath)
	if err != nil || probed.Name != path.Base(archivePath) || probed.Size != int64(len(archive)) {
		t.Fatalf("ProbeWorkloadArchivePath() = %#v, %v", probed, err)
	}

	var destination bytes.Buffer
	got, err := client.GetWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, archivePath, &destination, 1024,
	)
	if err != nil || !bytes.Equal(destination.Bytes(), archive) || got != probed {
		t.Fatalf("GetWorkloadArchive() = %#v, %q, %v", got, destination.Bytes(), err)
	}

	if err := client.PutWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, archivePath, bytes.NewReader(archive),
	); err != nil {
		t.Fatalf("PutWorkloadArchive() error = %v", err)
	}
}

func TestPodmanWorkloadArchiveRejectsInvalidEvidenceAndLimits(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerRunning,
	}
	client := connectedPodmanWorkloadClient(t, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/archive") {
			response.Header().Set(podmanContentType, podmanArchiveContentType)
			response.Header().Set("X-Docker-Container-Path-Stat", podmanArchiveStatHeader(t, "/data", 5))
			_, _ = response.Write([]byte("oversized"))

			return
		}
		state.handler(response, request)
	})

	var destination bytes.Buffer
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", &destination, 4,
	); !errors.Is(err, backup.ErrArchiveLimit) {
		t.Fatalf("GetWorkloadArchive(limit) error = %v", err)
	}
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data/../data", &destination, 4,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("GetWorkloadArchive(path) error = %v", err)
	}
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", nil, 4,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("GetWorkloadArchive(destination) error = %v", err)
	}
}

func TestPodmanArchiveHelpersRejectMalformedValues(t *testing.T) {
	t.Parallel()

	if _, err := decodePodmanArchivePathStat("not-base64"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanArchivePathStat(malformed) = %v", err)
	}
	invalidStat := base64.StdEncoding.EncodeToString([]byte(`{"name":"","size":-1}`))
	if _, err := decodePodmanArchivePathStat(invalidStat); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanArchivePathStat(invalid) = %v", err)
	}
	if err := copyPodmanArchive(nil, io.Discard, 1); !errors.Is(err, ErrProtocol) {
		t.Fatalf("copyPodmanArchive(nil) = %v", err)
	}
	response := &http.Response{Body: io.NopCloser(strings.NewReader("x"))}
	if err := copyPodmanArchive(response, io.Discard, 0); !errors.Is(err, ErrProtocol) {
		t.Fatalf("copyPodmanArchive(zero limit) = %v", err)
	}
}

//nolint:cyclop,funlen // The table covers independent malformed archive boundaries.
func TestPodmanArchiveHelperErrorMatrix(t *testing.T) {
	t.Parallel()

	if _, err := decodePodmanArchivePathStat(""); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanArchivePathStat(empty) = %v", err)
	}
	unknownStat := base64.StdEncoding.EncodeToString([]byte(`{"unknown":true}`))
	if _, err := decodePodmanArchivePathStat(unknownStat); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanArchivePathStat(unknown) = %v", err)
	}
	if err := copyPodmanArchive(&http.Response{
		ContentLength: 2, Body: io.NopCloser(strings.NewReader("xx")),
	}, io.Discard, 1); !errors.Is(err, backup.ErrArchiveLimit) {
		t.Fatalf("copyPodmanArchive(content length) = %v", err)
	}
	if err := copyPodmanArchive(&http.Response{
		ContentLength: -2, Body: io.NopCloser(strings.NewReader("x")),
	}, io.Discard, 1); !errors.Is(err, ErrProtocol) {
		t.Fatalf("copyPodmanArchive(invalid content length) = %v", err)
	}
	if err := copyPodmanArchive(&http.Response{
		ContentLength: -1, Body: &podmanArchiveTestBody{readErr: io.ErrUnexpectedEOF},
	}, io.Discard, 10); err == nil {
		t.Fatal("copyPodmanArchive(read) returned nil")
	}
	if err := copyPodmanArchive(&http.Response{
		ContentLength: -1, Body: &podmanArchiveTestBody{data: []byte("x"), closeErr: io.ErrClosedPipe},
	}, io.Discard, 10); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyPodmanArchive(close) = %v", err)
	}
	if err := copyPodmanArchive(&http.Response{
		ContentLength: -1, Body: io.NopCloser(strings.NewReader("xx")),
	}, io.Discard, 1); !errors.Is(err, backup.ErrArchiveLimit) {
		t.Fatalf("copyPodmanArchive(written limit) = %v", err)
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
			Header:     http.Header{podmanContentType: {podmanJSONType}},
			Body: io.NopCloser(strings.NewReader(fmt.Sprintf(
				`{"cause":"archive failed","message":"archive failed","response":%d}`,
				test.status,
			))),
		}
		if err := podmanArchiveResponseError(response); !errors.Is(err, test.want) {
			t.Errorf("podmanArchiveResponseError(%d) = %v", test.status, err)
		}
	}
}

func TestPodmanWorkloadArchiveHeadUsesStatusWithoutBody(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerRunning,
	}
	client := connectedPodmanWorkloadClient(t, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/archive") {
			response.Header().Set(podmanContentType, podmanJSONType)
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response,
				`{"cause":"path missing","message":"path missing","response":404}`)

			return
		}
		state.handler(response, request)
	})

	_, err := client.ProbeWorkloadArchivePath(
		context.Background(), workload, podmanTestTransaction, "/data",
	)
	if !errors.Is(err, application.ErrArchivePathMissing) {
		t.Fatalf("ProbeWorkloadArchivePath() error = %v", err)
	}
}

type podmanArchiveTestBody struct {
	data     []byte
	readErr  error
	closeErr error
}

func (body *podmanArchiveTestBody) Read(value []byte) (int, error) {
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

func (body *podmanArchiveTestBody) Close() error { return body.closeErr }

func podmanArchiveStatHeader(t *testing.T, archivePath string, size int64) string {
	t.Helper()

	raw, err := json.Marshal(struct {
		Name       string      `json:"name"`
		Size       int64       `json:"size"`
		Mode       os.FileMode `json:"mode"`
		ModTime    time.Time   `json:"mtime"`
		IsDir      bool        `json:"isDir"`      //nolint:tagliatelle // Native Libpod archive API wire field.
		LinkTarget string      `json:"linkTarget"` //nolint:tagliatelle // Docker archive API wire field.
	}{
		Name: path.Base(archivePath), Size: size, Mode: os.ModeDir | 0o755,
		ModTime: time.Unix(100, 0).UTC(), IsDir: true, LinkTarget: archivePath,
	})
	if err != nil {
		t.Fatal(err)
	}

	return base64.URLEncoding.EncodeToString(raw)
}
