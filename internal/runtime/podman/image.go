package podman

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const sha256HexLength = 64

type imageInspectResponse struct {
	ID           string          `json:"Id"`           //nolint:tagliatelle // Libpod wire field.
	Digest       string          `json:"Digest"`       //nolint:tagliatelle // Libpod wire field.
	RepoDigests  []string        `json:"RepoDigests"`  //nolint:tagliatelle // Libpod wire field.
	OS           string          `json:"Os"`           //nolint:tagliatelle // Libpod wire field.
	Architecture string          `json:"Architecture"` //nolint:tagliatelle // Libpod wire field.
	Variant      string          `json:"Variant"`      //nolint:tagliatelle // Libpod wire field.
	Size         int64           `json:"Size"`         //nolint:tagliatelle // Libpod wire field.
	Config       json.RawMessage `json:"Config"`       //nolint:tagliatelle // Libpod wire field.
}

// ProbeImage inspects one exact registry digest and platform through the native
// Libpod route. Only a valid JSON 404 proves absence.
func (client *Client) ProbeImage(
	ctx context.Context,
	expected domain.ImageIdentity,
) (application.ImageProbe, error) {
	var unknown application.ImageProbe

	reference, valid := client.validImage(expected)
	if !valid {
		return unknown, ErrUnsupportedWorkload
	}
	response, err := client.request(
		ctx,
		http.MethodGet,
		libpodPrefix+"/images/"+reference.DigestReference()+"/json",
		nil,
		nil,
		false,
	)
	if err != nil {
		return unknown, err
	}
	defer closePodmanResponse(response)

	if response.StatusCode == http.StatusNotFound {
		if !decodePodmanNotFound(response) {
			return unknown, ErrProtocol
		}

		return application.ImageProbe{State: application.ImageProbeMissing, Image: emptyImageEvidence()}, nil
	}
	if response.StatusCode != http.StatusOK || !isPodmanJSON(response.Header.Get(podmanContentType)) {
		return unknown, ErrProtocol
	}
	payload, valid := decodePodmanImage(response.Body)
	if !valid {
		return unknown, ErrProtocol
	}
	evidence, valid := podmanImageEvidence(payload, reference, expected)
	if !valid {
		return unknown, ErrProtocol
	}

	return application.ImageProbe{State: application.ImageProbeObserved, Image: evidence}, nil
}

func (client *Client) validImage(expected domain.ImageIdentity) (imageref.Reference, bool) {
	var empty imageref.Reference

	if client == nil || client.version.Protocol != libpodAPIVersion || expected.Origin != domain.ImageOriginRegistry {
		return empty, false
	}
	reference, err := imageref.Parse(expected.Reference)
	platform, platformValid := podmanPlatform(client.version.OS, client.version.Architecture)
	emptyDigest := domain.Digest{}
	if err != nil || !platformValid || expected.Platform != platform || reference.Digest() != expected.ReferenceDigest ||
		expected.PlatformManifest == emptyDigest || expected.ImageConfig == emptyDigest {
		return empty, false
	}

	return reference, true
}

func podmanImageEvidence(
	payload imageInspectResponse,
	reference imageref.Reference,
	expected domain.ImageIdentity,
) (application.ImageEvidence, bool) {
	var empty application.ImageEvidence

	imageConfig, configValid := podmanImageID(payload.ID)
	observedDigest, digestErr := domain.ParseDigest(payload.Digest)
	configObject := make(map[string]json.RawMessage)
	if !validPodmanImageCore(payload, expected, imageConfig, observedDigest, configValid, digestErr) ||
		!decodeRawJSONObject(payload.Config, &configObject) ||
		!validRepoDigests(payload.RepoDigests, reference, expected.PlatformManifest) {
		return empty, false
	}

	return application.ImageEvidence{
		ReferenceDigest: expected.ReferenceDigest, PlatformManifest: expected.PlatformManifest,
		ImageConfig: imageConfig, Platform: expected.Platform,
	}, true
}

func validPodmanImageCore(
	payload imageInspectResponse,
	expected domain.ImageIdentity,
	imageConfig domain.Digest,
	observedDigest domain.Digest,
	configValid bool,
	digestErr error,
) bool {
	return configValid && digestErr == nil &&
		(observedDigest == expected.ReferenceDigest || observedDigest == expected.PlatformManifest) &&
		imageConfig == expected.ImageConfig && payload.OS == expected.Platform.OS &&
		payload.Architecture == expected.Platform.Architecture &&
		(payload.Variant == "" || payload.Variant == expected.Platform.Variant) && payload.Size >= 0
}

func decodePodmanImage(reader io.Reader) (imageInspectResponse, bool) {
	var payload imageInspectResponse

	document, valid := jsonstrict.Read(reader, maximumControlBytes)
	var fields map[string]json.RawMessage
	if !valid || json.Unmarshal(document, &fields) != nil || json.Unmarshal(document, &payload) != nil {
		return imageInspectResponse{}, false
	}
	for _, name := range []string{"Id", "Digest", "RepoDigests", "Os", "Architecture", "Size", "Config"} {
		if _, present := fields[name]; !present {
			return imageInspectResponse{}, false
		}
	}

	return payload, true
}

func decodeRawJSONObject(value json.RawMessage, target *map[string]json.RawMessage) bool {
	return target != nil && len(value) > 0 && jsonstrict.Decode(bytes.NewReader(value), maximumControlBytes, target)
}

func podmanImageID(value string) (domain.Digest, bool) {
	if len(value) == sha256HexLength {
		value = "sha256:" + value
	}
	digest, err := domain.ParseDigest(value)

	return digest, err == nil
}

func validRepoDigests(
	values []string,
	expected imageref.Reference,
	platformManifest domain.Digest,
) bool {
	expectedRepository, _, _ := strings.Cut(expected.DigestReference(), "@")
	referenceFound := false
	platformFound := false
	for _, value := range values {
		candidate, err := imageref.Parse(value)
		if err != nil {
			return false
		}
		repository, _, _ := strings.Cut(candidate.DigestReference(), "@")
		if repository != expectedRepository {
			continue
		}
		digest := candidate.Digest()
		if digest != expected.Digest() && digest != platformManifest {
			return false
		}
		if digest == expected.Digest() {
			referenceFound = true
		}
		if digest == platformManifest {
			platformFound = true
		}
	}

	return referenceFound && platformFound
}

type podmanErrorResponse struct {
	Cause        string `json:"cause"`
	Message      string `json:"message"`
	ResponseCode int    `json:"response"`
}

func decodePodmanNotFound(response *http.Response) bool {
	if response == nil || response.Body == nil || !isPodmanJSON(response.Header.Get(podmanContentType)) {
		return false
	}
	var payload podmanErrorResponse

	return decodePodmanJSON(response.Body, maximumControlBytes, &payload) &&
		payload.ResponseCode == http.StatusNotFound && validPodmanText(payload.Cause) && validPodmanText(payload.Message)
}

func emptyImageEvidence() application.ImageEvidence {
	return application.ImageEvidence{
		ReferenceDigest: domain.Digest{}, PlatformManifest: domain.Digest{}, ImageConfig: domain.Digest{},
		Platform: domain.Platform{OS: "", Architecture: "", Variant: ""},
	}
}
