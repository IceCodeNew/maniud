package docker

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	imagetypes "github.com/moby/moby/api/types/image"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

// ProbeImage inspects one exact digest-pinned platform. Only a valid 404 proves
// absence; all transport, protocol, and identity conflicts remain unknown.
func (client *Client) ProbeImage(
	ctx context.Context,
	expected domain.ImageIdentity,
) (application.ImageProbe, error) {
	var unknown application.ImageProbe

	if client == nil {
		return unknown, ErrUnsupportedWorkload
	}

	reference, err := imageref.Parse(expected.Reference)
	if err != nil || !validDockerImage(client.version, expected) {
		return unknown, ErrUnsupportedWorkload
	}

	path, valid := client.versionedPath("/images/" + reference.DigestReference() + "/json")
	if !valid {
		return unknown, ErrProtocol
	}

	response, err := client.requestQuery(ctx, http.MethodGet, path, url.Values{
		"platform": []string{imagePlatform(expected.Platform)},
	})
	if err != nil {
		return unknown, err
	}
	defer closeResponse(response)

	return decodeImageResponse(response, reference, expected)
}

func decodeImageResponse(
	response *http.Response,
	reference imageref.Reference,
	expected domain.ImageIdentity,
) (application.ImageProbe, error) {
	var unknown application.ImageProbe

	if response.StatusCode == http.StatusNotFound {
		if !validNotFoundResponse(response) {
			return unknown, ErrProtocol
		}

		return application.ImageProbe{State: application.ImageProbeMissing, Image: emptyImage()}, nil
	}

	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get("Content-Type")) {
		return unknown, ErrProtocol
	}

	var payload imagetypes.InspectResponse
	if !decodeStrictJSON(response.Body, &payload) {
		return unknown, ErrProtocol
	}

	observed, valid := decodeImage(payload, reference, expected)
	if !valid {
		return unknown, ErrProtocol
	}

	return application.ImageProbe{State: application.ImageProbeObserved, Image: observed}, nil
}

func emptyImage() application.ImageEvidence {
	return application.ImageEvidence{
		ReferenceDigest:  domain.Digest{},
		PlatformManifest: domain.Digest{},
		ImageConfig:      domain.Digest{},
		Platform:         domain.Platform{OS: "", Architecture: "", Variant: ""},
	}
}

func imagePlatform(platform domain.Platform) string {
	value := "{\"architecture\":" + strconv.Quote(platform.Architecture) +
		",\"os\":" + strconv.Quote(platform.OS)
	if platform.Variant != "" {
		value += ",\"variant\":" + strconv.Quote(platform.Variant)
	}

	return value + "}"
}

func decodeImage(
	payload imagetypes.InspectResponse,
	reference imageref.Reference,
	expected domain.ImageIdentity,
) (application.ImageEvidence, bool) {
	var empty application.ImageEvidence

	imageConfig, configErr := domain.ParseDigest(payload.ID)
	if configErr != nil || imageConfig != expected.ImageConfig || payload.Config == nil ||
		payload.Os != expected.Platform.OS || payload.Architecture != expected.Platform.Architecture ||
		payload.Variant != expected.Platform.Variant || payload.Size < 0 ||
		!hasReferenceDigest(payload.RepoDigests, reference.DigestReference()) ||
		!validImageTarget(payload, expected.ReferenceDigest, expected.PlatformManifest) {
		return empty, false
	}

	return application.ImageEvidence{
		ReferenceDigest:  expected.ReferenceDigest,
		PlatformManifest: expected.PlatformManifest,
		ImageConfig:      imageConfig,
		Platform:         expected.Platform,
	}, true
}

func hasReferenceDigest(values []string, expected string) bool {
	found := false

	for _, value := range values {
		reference, err := imageref.Parse(value)
		if err != nil {
			return false
		}

		if reference.DigestReference() == expected {
			found = true
		}
	}

	return found
}

func validImageTarget(
	payload imagetypes.InspectResponse,
	referenceDigest domain.Digest,
	platformManifest domain.Digest,
) bool {
	if payload.Descriptor == nil {
		return referenceDigest == platformManifest
	}

	digest, err := domain.ParseDigest(payload.Descriptor.Digest.String())

	return err == nil && digest == platformManifest &&
		validManifestDescriptor(payload.Descriptor.MediaType, payload.Descriptor.Size)
}
