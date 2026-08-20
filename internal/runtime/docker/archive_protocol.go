package docker

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const maximumArchivePathBytes = 4096

func copyDockerArchive(response *http.Response, destination io.Writer, maximumBytes int64) error {
	if response == nil || response.Body == nil {
		closeResponse(response)

		return ErrProtocol
	}
	if response.ContentLength < -1 {
		closeResponse(response)

		return ErrProtocol
	}
	if response.ContentLength > maximumBytes {
		closeResponse(response)

		return backup.ErrArchiveLimit
	}
	if response.ContentLength == 0 {
		closeResponse(response)

		return ErrProtocol
	}

	written, copyErr := io.Copy(destination, io.LimitReader(response.Body, maximumBytes+1))
	closeErr := response.Body.Close()
	if copyErr != nil {
		return fmt.Errorf("copy Docker archive: %w", copyErr)
	}
	if closeErr != nil {
		return ErrUnavailable
	}
	if written > maximumBytes {
		return backup.ErrArchiveLimit
	}

	return nil
}

func (client *Client) archiveRequest(
	ctx context.Context,
	method string,
	pathValue string,
	query url.Values,
	body io.Reader,
) (*http.Response, error) {
	endpoint := client.baseURL
	endpoint.Path = pathValue
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), body)
	if err != nil {
		return nil, ErrProtocol
	}
	request.Header.Set("Accept", dockerArchiveContentType)
	request.Header.Set("Accept-Encoding", "identity")
	if body != nil {
		request.Header.Set(contentTypeHeader, dockerArchiveContentType)
	}
	streamClient := *client.httpClient
	streamClient.Timeout = 0
	response, err := streamClient.Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("docker archive request: %w", ctxErr)
		}

		return nil, ErrUnavailable
	}

	return response, nil
}

func validateDockerArchiveResponse(
	response *http.Response,
	archivePath string,
) (application.ArchivePathStat, error) {
	var empty application.ArchivePathStat

	if response == nil {
		return empty, ErrProtocol
	}
	if response.StatusCode != http.StatusOK {
		return empty, dockerArchiveResponseError(response)
	}
	if !isDockerArchive(response.Header.Get(contentTypeHeader)) ||
		!validIdentityEncoding(response.Header.Get("Content-Encoding")) {
		closeResponse(response)

		return empty, ErrProtocol
	}

	stat, err := decodeDockerArchivePathStat(response.Header.Get("X-Docker-Container-Path-Stat"))
	if err != nil || stat.Name != path.Base(archivePath) {
		closeResponse(response)

		return empty, ErrProtocol
	}

	return stat, nil
}

func decodeDockerArchivePathStat(encoded string) (application.ArchivePathStat, error) {
	var empty application.ArchivePathStat

	if encoded == "" {
		return empty, ErrProtocol
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return empty, ErrProtocol
	}
	var payload containertypes.PathStat
	if !jsonstrict.Decode(strings.NewReader(string(raw)), maximumJSONBytes, &payload) ||
		!validArchivePathStat(payload) {
		return empty, ErrProtocol
	}

	return application.ArchivePathStat{
		Name: payload.Name, Size: payload.Size, Mode: payload.Mode,
		ModTime: payload.Mtime, LinkTarget: payload.LinkTarget,
	}, nil
}

func validArchivePathStat(value containertypes.PathStat) bool {
	return validArchiveText(value.Name) && value.Size >= 0 && validOptionalArchiveText(value.LinkTarget)
}

func validArchiveText(value string) bool {
	return value != "" && len(value) <= maximumArchivePathBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}

func validOptionalArchiveText(value string) bool {
	return value == "" || validArchiveText(value)
}

func validArchivePath(value string) bool {
	return validArchiveText(value) && strings.HasPrefix(value, "/") &&
		path.Clean(value) == value && !strings.Contains(value, "\\")
}

func validIdentityEncoding(value string) bool {
	return value == "" || value == "identity"
}

func decodeDockerArchivePutResponse(response *http.Response) error {
	if response == nil {
		return ErrProtocol
	}
	if response.StatusCode != http.StatusOK {
		return dockerArchiveResponseError(response)
	}
	if response.Body == nil {
		closeResponse(response)

		return ErrProtocol
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if len(body) != 0 {
		return ErrProtocol
	}

	return nil
}

func dockerArchiveResponseError(response *http.Response) error {
	if response == nil || response.Body == nil || !validErrorResponse(response) {
		closeResponse(response)

		return ErrProtocol
	}
	closeResponse(response)

	switch response.StatusCode {
	case http.StatusBadRequest, http.StatusForbidden:
		return application.ErrArchiveConflict
	case http.StatusNotFound:
		return application.ErrArchivePathMissing
	case http.StatusInternalServerError:
		return ErrUnavailable
	default:
		return ErrProtocol
	}
}
