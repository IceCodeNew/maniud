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
	maximumDaemonIDBytes     = 256
	maximumDriverBytes       = 128
	maximumArchitectureBytes = 64
	rootlessSecurityOption   = "name=rootless"
	dockerMachineAMD64       = "x86_64"
	dockerMachineARM64       = "aarch64"
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

	if client == nil {
		return emptyDaemon, ErrProtocol
	}

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
	if !decodeStrictJSON(response.Body, &payload) {
		return emptyDaemon, ErrProtocol
	}

	architecture, valid := daemonArchitecture(payload.Architecture, client.version.Architecture)
	if !valid || !validDaemonInfo(payload, client.version) {
		return emptyDaemon, ErrProtocol
	}

	return Daemon{
		ID:           payload.ID,
		Driver:       payload.Driver,
		OS:           payload.OSType,
		Architecture: architecture,
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
		info.OSType == version.OS && info.ServerVersion == version.Product
}

func daemonArchitecture(machine, binary string) (string, bool) {
	switch machine {
	case dockerMachineAMD64:
		machine = dockerArchitectureAMD64
	case dockerMachineARM64:
		machine = dockerArchitectureARM64
	}

	return machine, validOpaqueValue(machine, maximumArchitectureBytes) && machine == binary
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
