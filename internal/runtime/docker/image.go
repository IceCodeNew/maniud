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
	switch expected.Origin {
	case domain.ImageOriginRegistry:
		return client.probeRegistryImage(ctx, expected)
	case domain.ImageOriginDockerArchive:
		if !validArchiveImage(client.version, expected) {
			return unknown, ErrUnsupportedWorkload
		}

		return client.probeArchiveImage(ctx, expected)
	case domain.ImageOriginUnknown:
		return unknown, ErrUnsupportedWorkload
	default:
		return unknown, ErrUnsupportedWorkload
	}
}

func (client *Client) probeRegistryImage(
	ctx context.Context,
	expected domain.ImageIdentity,
) (application.ImageProbe, error) {
	var unknown application.ImageProbe

	reference, err := imageref.Parse(expected.Reference)
	if err != nil || !validRegistryImage(client.version, expected) {
		return unknown, ErrUnsupportedWorkload
	}

	path, valid := client.versionedPath("/images/" + reference.DigestReference() + "/json")
	if !valid {
		return unknown, ErrProtocol
	}

	response, err := client.requestQuery(ctx, http.MethodGet, path, url.Values{
		imagePullPlatformQuery: []string{imagePlatform(expected.Platform)},
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
		if !validErrorResponse(response) {
			return unknown, ErrProtocol
		}

		return application.ImageProbe{State: application.ImageProbeMissing, Image: emptyImage()}, nil
	}

	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get(contentTypeHeader)) {
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

	if !validImageID(payload.ID, expected, payload.Descriptor != nil) || payload.Config == nil ||
		payload.Os != expected.Platform.OS || payload.Architecture != expected.Platform.Architecture ||
		payload.Variant != expected.Platform.Variant || payload.Size < 0 ||
		!hasReferenceDigest(payload.RepoDigests, reference) ||
		!validImageTarget(payload, expected.ReferenceDigest, expected.PlatformManifest) {
		return empty, false
	}

	return application.ImageEvidence{
		ReferenceDigest:  expected.ReferenceDigest,
		PlatformManifest: expected.PlatformManifest,
		ImageConfig:      expected.ImageConfig,
		Platform:         expected.Platform,
	}, true
}

func validImageID(value string, expected domain.ImageIdentity, hasDescriptor bool) bool {
	identifier, err := domain.ParseDigest(value)

	// The classic image store reports the config digest as Id. The containerd
	// image store reports the selected manifest digest and supplies its OCI
	// descriptor, which transitively binds the registry-proven config digest.
	return err == nil && (identifier == expected.ImageConfig ||
		hasDescriptor && identifier == expected.PlatformManifest)
}

func hasReferenceDigest(values []string, expected imageref.Reference) bool {
	found := false

	for _, value := range values {
		source, err := imageref.Normalize(value)
		if err != nil || !source.IsPinned() {
			return false
		}
		reference, err := source.Pin(expected.Digest())
		if err != nil {
			return false
		}

		if reference.DigestReference() == expected.DigestReference() {
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
