package podman

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const maximumArchivePathBytes = 4096

type podmanArchivePathStat struct {
	Name       string      `json:"name"`
	Size       int64       `json:"size"`
	Mode       os.FileMode `json:"mode"`
	ModTime    time.Time   `json:"mtime"`
	IsDir      bool        `json:"isDir"`      //nolint:tagliatelle // Native Libpod archive API wire field.
	LinkTarget string      `json:"linkTarget"` //nolint:tagliatelle // Docker archive API wire field.
}

func copyPodmanArchive(response *http.Response, destination io.Writer, maximumBytes int64) error {
	if response == nil || response.Body == nil {
		closePodmanResponse(response)

		return ErrProtocol
	}
	if response.ContentLength < -1 {
		closePodmanResponse(response)

		return ErrProtocol
	}
	if response.ContentLength > maximumBytes {
		closePodmanResponse(response)

		return backup.ErrArchiveLimit
	}
	if response.ContentLength == 0 {
		closePodmanResponse(response)

		return ErrProtocol
	}

	written, copyErr := io.Copy(destination, io.LimitReader(response.Body, maximumBytes+1))
	closeErr := response.Body.Close()
	if copyErr != nil {
		return fmt.Errorf("copy Podman archive: %w", copyErr)
	}
	if closeErr != nil {
		return ErrUnavailable
	}
	if written > maximumBytes {
		return backup.ErrArchiveLimit
	}

	return nil
}

func validatePodmanArchiveResponse(
	response *http.Response,
	archivePath string,
) (application.ArchivePathStat, error) {
	var empty application.ArchivePathStat

	if response == nil {
		return empty, ErrProtocol
	}
	if response.StatusCode != http.StatusOK {
		return empty, podmanArchiveResponseError(response)
	}
	if !isArchiveContentType(response.Header.Get(podmanContentType)) ||
		!validIdentityEncoding(response.Header.Get("Content-Encoding")) {
		closePodmanResponse(response)

		return empty, ErrProtocol
	}

	stat, err := decodePodmanArchivePathStat(response.Header.Get("X-Docker-Container-Path-Stat"))
	if err != nil || stat.Name != path.Base(archivePath) {
		closePodmanResponse(response)

		return empty, ErrProtocol
	}

	return stat, nil
}

func decodePodmanArchivePathStat(encoded string) (application.ArchivePathStat, error) {
	var empty application.ArchivePathStat

	if encoded == "" {
		return empty, ErrProtocol
	}
	raw, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		return empty, ErrProtocol
	}
	var payload podmanArchivePathStat
	if !jsonstrict.Decode(bytes.NewReader(raw), maximumControlBytes, &payload) ||
		!validArchivePathStat(payload) {
		return empty, ErrProtocol
	}

	return application.ArchivePathStat{
		Name: payload.Name, Size: payload.Size, Mode: payload.Mode,
		ModTime: payload.ModTime, LinkTarget: payload.LinkTarget,
	}, nil
}

func validArchivePathStat(value podmanArchivePathStat) bool {
	return validArchiveText(value.Name) && value.Size >= 0 && validOptionalArchiveText(value.LinkTarget) &&
		value.IsDir == value.Mode.IsDir()
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

func isArchiveContentType(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)

	return err == nil && mediaType == podmanArchiveContentType
}

func podmanArchiveResponseError(response *http.Response) error {
	if !validPodmanArchiveErrorResponse(response) {
		return ErrProtocol
	}

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

func validPodmanArchiveErrorResponse(response *http.Response) bool {
	if response == nil || response.Body == nil || !isPodmanJSON(response.Header.Get(podmanContentType)) {
		closePodmanResponse(response)

		return false
	}
	var payload podmanErrorResponse
	valid := jsonstrict.Decode(response.Body, maximumControlBytes, &payload) &&
		payload.ResponseCode == response.StatusCode &&
		validPodmanText(payload.Cause) && validPodmanText(payload.Message)
	closePodmanResponse(response)

	return valid
}
