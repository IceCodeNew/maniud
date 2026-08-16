package docker

import (
	"context"
	"net/http"
	"slices"
	"unicode"
	"unicode/utf8"

	"github.com/moby/moby/api/types/system"
)

const (
	maximumDaemonIDBytes   = 256
	maximumDriverBytes     = 128
	rootlessSecurityOption = "name=rootless"
)

// Daemon is the typed identity and minimum capability evidence for one Docker Engine.
type Daemon struct {
	ID           string
	Driver       string
	OS           string
	Architecture string
	Rootless     bool
}

// InspectDaemon reads identity and platform evidence from the negotiated Engine.
func (client *Client) InspectDaemon(ctx context.Context) (Daemon, error) {
	var emptyDaemon Daemon

	path, valid := client.versionedPath("/info")
	if !valid {
		return emptyDaemon, ErrProtocol
	}

	response, err := client.request(ctx, http.MethodGet, path)
	if err != nil {
		return emptyDaemon, err
	}
	defer closeResponse(response)

	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get(contentTypeHeader)) {
		return emptyDaemon, ErrProtocol
	}

	var payload system.Info
	if !decodeStrictJSON(response.Body, &payload) || !validDaemonInfo(payload, client.version) {
		return emptyDaemon, ErrProtocol
	}

	return Daemon{
		ID:           payload.ID,
		Driver:       payload.Driver,
		OS:           payload.OSType,
		Architecture: payload.Architecture,
		Rootless:     slices.Contains(payload.SecurityOptions, rootlessSecurityOption),
	}, nil
}

func (client *Client) versionedPath(path string) (string, bool) {
	protocol, valid := parseAPIVersion(client.version.Protocol)
	if !valid || protocol.String() != client.version.Protocol || path == "" || path[0] != '/' {
		return "", false
	}

	return "/v" + protocol.String() + path, true
}

func validDaemonInfo(info system.Info, version Version) bool {
	return validOpaqueValue(info.ID, maximumDaemonIDBytes) && validOpaqueValue(info.Driver, maximumDriverBytes) &&
		info.OSType == version.OS && info.Architecture == version.Architecture &&
		info.ServerVersion == version.Product
}

func validOpaqueValue(value string, limit int) bool {
	if value == "" || len(value) > limit || !utf8.ValidString(value) {
		return false
	}

	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}

	return true
}
