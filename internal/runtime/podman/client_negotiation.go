package podman

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	maximumPingBytes     = int64(1024)
	maximumVersionBytes  = int64(1 << 20)
	maximumTextBytes     = 4096
	semanticVersionParts = 3
	podmanAPIHeader      = "Libpod-Api-Version"
	podmanOSLinux        = "linux"
	podmanArchAMD64      = "amd64"
	podmanArchARM64      = "arm64"
)

func (client *Client) negotiate(ctx context.Context) (Version, domain.Digest, error) {
	var empty Version
	var emptyDigest domain.Digest

	maximum, err := client.ping(ctx)
	if err != nil {
		return empty, emptyDigest, err
	}
	version, err := client.serverVersion(ctx, maximum)
	if err != nil {
		return empty, emptyDigest, err
	}
	scope, err := client.inspectScope(ctx, version)
	if err != nil {
		return empty, emptyDigest, err
	}
	version = client.version

	return version, scope, nil
}

func (client *Client) ping(ctx context.Context) (string, error) {
	response, err := client.request(ctx, http.MethodGet, "/_ping", nil, nil, false)
	if err != nil {
		return "", err
	}
	defer closePodmanResponse(response)

	value, readErr := io.ReadAll(io.LimitReader(response.Body, maximumPingBytes+1))
	maximum := response.Header.Get(podmanAPIHeader)
	if readErr != nil || response.StatusCode != http.StatusOK || len(value) > int(maximumPingBytes) ||
		string(bytes.TrimSpace(value)) != "OK" || !validSemanticVersion(maximum) {
		return "", ErrProtocol
	}

	return maximum, nil
}

type versionResponse struct {
	Version    string             `json:"Version"`    //nolint:tagliatelle // Libpod wire field.
	Components []versionComponent `json:"Components"` //nolint:tagliatelle // Libpod wire field.
}

type versionComponent struct {
	Name    string            `json:"Name"`    //nolint:tagliatelle // Libpod wire field.
	Version string            `json:"Version"` //nolint:tagliatelle // Libpod wire field.
	Details map[string]string `json:"Details"` //nolint:tagliatelle // Libpod wire field.
}

func (client *Client) serverVersion(ctx context.Context, pingMaximum string) (Version, error) {
	var empty Version

	response, err := client.request(ctx, http.MethodGet, "/version", nil, nil, false)
	if err != nil {
		return empty, err
	}
	defer closePodmanResponse(response)

	var payload versionResponse
	if response.StatusCode != http.StatusOK || !isPodmanJSON(response.Header.Get(podmanContentType)) ||
		!decodePodmanJSON(response.Body, maximumVersionBytes, &payload) ||
		!validPodmanText(payload.Version) {
		return empty, ErrProtocol
	}
	engine, valid := podmanEngine(payload.Components)
	if !valid || engine.Version != payload.Version {
		return empty, ErrProtocol
	}
	serverMaximum := engine.Details["APIVersion"]
	serverMinimum := engine.Details["MinAPIVersion"]
	if serverMaximum != pingMaximum || !compatibleLibpodRange(serverMinimum, serverMaximum) {
		return empty, ErrProtocol
	}

	return Version{
		Protocol: libpodAPIVersion, Minimum: serverMinimum, Maximum: serverMaximum,
		Product: payload.Version, OS: "", Architecture: "",
	}, nil
}

func podmanEngine(components []versionComponent) (versionComponent, bool) {
	var engine versionComponent
	found := false
	for _, component := range components {
		if component.Name != "Podman Engine" {
			continue
		}
		if found {
			return versionComponent{}, false
		}
		engine = component
		found = true
	}

	return engine, found
}

type infoResponse struct {
	Host struct {
		OS   string `json:"os"`
		Arch string `json:"arch"`
	} `json:"host"`
	Store struct {
		GraphRoot string `json:"graphRoot"` //nolint:tagliatelle // Libpod wire field.
	} `json:"store"`
}

func (client *Client) inspectScope(ctx context.Context, version Version) (domain.Digest, error) {
	response, err := client.request(ctx, http.MethodGet, libpodPrefix+"/info", nil, nil, false)
	if err != nil {
		return domain.Digest{}, err
	}
	defer closePodmanResponse(response)

	payload, platform, err := decodePodmanInfo(response)
	if err != nil {
		return domain.Digest{}, err
	}
	client.peerLock.Lock()
	peer := client.peer
	client.peerLock.Unlock()
	if peer == (peerIdentity{}) {
		return domain.Digest{}, ErrInvalidEndpoint
	}

	evidence := client.scopeEvidence(payload, platform, version, peer)

	version.OS = platform.OS
	version.Architecture = platform.Architecture
	client.version = version

	return domain.Hash(evidence), nil
}

func decodePodmanInfo(response *http.Response) (infoResponse, domain.Platform, error) {
	var payload infoResponse
	if response.StatusCode != http.StatusOK || !isPodmanJSON(response.Header.Get(podmanContentType)) ||
		!decodePodmanJSON(response.Body, maximumControlBytes, &payload) ||
		!validPodmanText(payload.Host.OS) || !validPodmanText(payload.Host.Arch) ||
		!validGraphRoot(payload.Store.GraphRoot) {
		return infoResponse{}, domain.Platform{}, ErrProtocol
	}
	platform, valid := podmanPlatform(payload.Host.OS, payload.Host.Arch)
	if !valid {
		return infoResponse{}, domain.Platform{}, ErrUnsupportedWorkload
	}

	return payload, platform, nil
}

func validGraphRoot(value string) bool {
	return value != "" && filepath.IsAbs(value) && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func (client *Client) scopeEvidence(
	payload infoResponse,
	platform domain.Platform,
	version Version,
	peer peerIdentity,
) []byte {
	evidence := []byte{1}
	evidence = appendPodmanString(evidence, domain.RuntimePodman.String())
	evidence = appendPodmanString(evidence, version.Product)
	evidence = appendPodmanString(evidence, version.Protocol)
	evidence = appendPodmanString(evidence, platform.OS)
	evidence = appendPodmanString(evidence, platform.Architecture)
	evidence = appendPodmanString(evidence, platform.Variant)
	evidence = appendPodmanString(evidence, payload.Store.GraphRoot)
	evidence = binary.LittleEndian.AppendUint64(evidence, client.socket.device)
	evidence = binary.LittleEndian.AppendUint64(evidence, client.socket.inode)
	evidence = binary.LittleEndian.AppendUint32(evidence, client.socket.owner)
	evidence = binary.LittleEndian.AppendUint32(evidence, client.socket.mode)
	process := uint32(peer.process) //nolint:gosec // connectedPeer accepts positive int32 PIDs only.
	evidence = binary.LittleEndian.AppendUint32(evidence, process)
	evidence = binary.LittleEndian.AppendUint32(evidence, peer.owner)
	evidence = binary.LittleEndian.AppendUint32(evidence, peer.group)
	evidence = binary.LittleEndian.AppendUint64(evidence, peer.generation)

	return evidence
}

func decodePodmanJSON(reader io.Reader, maximum int64, target any) bool {
	value, valid := jsonstrict.Read(reader, maximum)
	if !valid {
		return false
	}

	return json.Unmarshal(value, target) == nil
}

func isPodmanJSON(value string) bool {
	mediaType, _, err := mime.ParseMediaType(value)

	return err == nil && mediaType == podmanJSONType
}

func compatibleLibpodRange(minimum, maximum string) bool {
	want, validWant := parseSemanticVersion(libpodAPIVersion)
	minimumVersion, validMinimum := parseSemanticVersion(minimum)
	maximumVersion, validMaximum := parseSemanticVersion(maximum)

	return validWant && validMinimum && validMaximum &&
		!want.less(minimumVersion) && !maximumVersion.less(want)
}

type semanticVersion struct {
	major uint64
	minor uint64
	patch uint64
}

func validSemanticVersion(value string) bool {
	_, valid := parseSemanticVersion(value)

	return valid
}

func parseSemanticVersion(value string) (semanticVersion, bool) {
	var empty semanticVersion
	parts := strings.Split(value, ".")
	if len(parts) != semanticVersionParts {
		return empty, false
	}
	values := [3]uint64{}
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return empty, false
		}
		parsed, err := strconv.ParseUint(part, 10, 31)
		if err != nil {
			return empty, false
		}
		values[index] = parsed
	}

	return semanticVersion{major: values[0], minor: values[1], patch: values[2]}, true
}

func (version semanticVersion) less(other semanticVersion) bool {
	if version.major != other.major {
		return version.major < other.major
	}
	if version.minor != other.minor {
		return version.minor < other.minor
	}

	return version.patch < other.patch
}

func validPodmanText(value string) bool {
	return value != "" && len(value) <= maximumTextBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}

func appendPodmanString(encoded []byte, value string) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(value)))

	return append(encoded, value...)
}

func podmanPlatform(osName, architecture string) (domain.Platform, bool) {
	if osName != podmanOSLinux {
		return domain.Platform{}, false
	}
	switch architecture {
	case podmanArchAMD64:
		return domain.Platform{OS: osName, Architecture: architecture, Variant: ""}, true
	case podmanArchARM64:
		return domain.Platform{OS: osName, Architecture: architecture, Variant: "v8"}, true
	default:
		return domain.Platform{}, false
	}
}
