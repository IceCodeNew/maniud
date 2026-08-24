package docker

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"time"

	imagetypes "github.com/moby/moby/api/types/image"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/imagerootfs"
)

const (
	dockerArchiveContentType       = "application/x-tar"
	maximumDockerSaveBytes         = int64(1 << 40)
	maximumDockerSaveProofDuration = 10 * time.Minute
)

type archiveImageSnapshot struct {
	imageID             domain.Digest
	platform            domain.Platform
	platformManifest    domain.Digest
	hasPlatformManifest bool
}

func (client *Client) probeArchiveImage(
	ctx context.Context,
	expected domain.ImageIdentity,
) (application.ImageProbe, error) {
	var unknown application.ImageProbe

	before, state, err := client.inspectArchiveImage(ctx, expected)
	if err != nil {
		return unknown, err
	}
	if state == application.ImageProbeMissing {
		return application.ImageProbe{State: application.ImageProbeMissing, Image: emptyImage()}, nil
	}
	analysis, err := client.analyzeSavedArchive(ctx, expected.Reference, expected.Platform)
	if err != nil {
		return unknown, err
	}
	after, state, err := client.inspectArchiveImage(ctx, expected)
	if err != nil {
		return unknown, err
	}
	if state != application.ImageProbeObserved {
		return unknown, ErrProtocol
	}
	if before != after || !savedArchiveMatches(analysis, expected) {
		return unknown, ErrProtocol
	}

	return application.ImageProbe{
		State: application.ImageProbeObserved,
		Image: archiveImageEvidence(expected),
	}, nil
}

func (client *Client) analyzeSavedArchive(
	ctx context.Context,
	reference string,
	platform domain.Platform,
) (imagearchive.Analysis, error) {
	return client.analyzeSavedArchiveVisit(ctx, reference, platform, nil)
}

func (client *Client) analyzeSavedArchiveVisit(
	ctx context.Context,
	reference string,
	platform domain.Platform,
	visit func(context.Context, string) error,
) (imagearchive.Analysis, error) {
	var empty imagearchive.Analysis
	proofContext, cancel := context.WithTimeout(ctx, maximumDockerSaveProofDuration)
	defer cancel()

	response, err := client.saveArchiveImage(proofContext, reference, platform)
	if err != nil {
		return empty, err
	}
	if !validSavedArchiveResponse(response) {
		closeResponse(response)

		return empty, ErrProtocol
	}

	analysis, analyzeErr := imagearchive.AnalyzeStreamVisit(proofContext, response.Body, visit)
	closeErr := response.Body.Close()
	if analyzeErr != nil || closeErr != nil {
		if ctxErr := proofContext.Err(); ctxErr != nil {
			return empty, fmt.Errorf("prove Docker archive image: %w", ctxErr)
		}

		return empty, ErrProtocol
	}
	if response.ContentLength >= 0 && analysis.ArchiveSize != response.ContentLength {
		return empty, ErrProtocol
	}

	return analysis, nil
}

// ResolveImageUser resolves a named or incomplete image user against the
// verified final image filesystem without starting a container.
func (client *Client) ResolveImageUser(
	ctx context.Context,
	expected domain.ImageIdentity,
	specification string,
) (string, error) {
	before, err := client.ProbeImage(ctx, expected)
	if !observedImageMatches(before, expected, err) {
		return "", ErrProtocol
	}
	resolved := ""
	analysis, err := client.analyzeSavedArchiveVisit(
		ctx,
		expected.Reference,
		expected.Platform,
		func(visitContext context.Context, path string) error {
			var resolveErr error
			resolved, resolveErr = imagerootfs.ResolveArchiveUser(visitContext, path, specification)
			if resolveErr != nil {
				return fmt.Errorf("resolve Docker image user: %w", resolveErr)
			}

			return nil
		},
	)
	if err != nil || !savedRegistryArchiveMatches(analysis, expected) {
		return "", ErrProtocol
	}
	after, err := client.ProbeImage(ctx, expected)
	if !observedImageMatches(after, expected, err) || after != before {
		return "", ErrProtocol
	}

	return resolved, nil
}

func observedImageMatches(
	probe application.ImageProbe,
	expected domain.ImageIdentity,
	err error,
) bool {
	return err == nil && probe.State == application.ImageProbeObserved && probe.Matches(expected)
}

func savedRegistryArchiveMatches(analysis imagearchive.Analysis, expected domain.ImageIdentity) bool {
	return analysis.MemberIndex == 0 && analysis.Identity.ImageConfig == expected.ImageConfig &&
		analysis.Identity.Platform == expected.Platform
}

func validSavedArchiveResponse(response *http.Response) bool {
	return response.StatusCode == http.StatusOK && isDockerArchive(response.Header.Get(contentTypeHeader)) &&
		response.ContentLength >= -1 && response.ContentLength != 0 &&
		response.ContentLength <= maximumDockerSaveBytes
}

func archiveImageEvidence(expected domain.ImageIdentity) application.ImageEvidence {
	return application.ImageEvidence{
		ReferenceDigest:  expected.ReferenceDigest,
		PlatformManifest: expected.PlatformManifest,
		ImageConfig:      expected.ImageConfig,
		Platform:         expected.Platform,
	}
}

func (client *Client) inspectArchiveImage(
	ctx context.Context,
	expected domain.ImageIdentity,
) (archiveImageSnapshot, application.ImageProbeState, error) {
	var empty archiveImageSnapshot
	path, valid := client.versionedPath("/images/" + expected.Reference + "/json")
	if !valid {
		return empty, application.ImageProbeUnknown, ErrProtocol
	}
	response, err := client.requestQuery(ctx, http.MethodGet, path, url.Values{
		imagePullPlatformQuery: []string{imagePlatform(expected.Platform)},
	})
	if err != nil {
		return empty, application.ImageProbeUnknown, err
	}
	defer closeResponse(response)

	if response.StatusCode == http.StatusNotFound {
		if !validErrorResponse(response) {
			return empty, application.ImageProbeUnknown, ErrProtocol
		}

		return empty, application.ImageProbeMissing, nil
	}
	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get(contentTypeHeader)) {
		return empty, application.ImageProbeUnknown, ErrProtocol
	}

	var payload imagetypes.InspectResponse
	if !decodeStrictJSON(response.Body, &payload) {
		return empty, application.ImageProbeUnknown, ErrProtocol
	}
	snapshot, valid := archiveImageInspectSnapshot(payload, expected)
	if !valid {
		return empty, application.ImageProbeUnknown, ErrProtocol
	}

	return snapshot, application.ImageProbeObserved, nil
}

func archiveImageInspectSnapshot(
	payload imagetypes.InspectResponse,
	expected domain.ImageIdentity,
) (archiveImageSnapshot, bool) {
	var empty archiveImageSnapshot
	if !validArchiveImageInspect(payload, expected) {
		return empty, false
	}

	snapshot := archiveImageSnapshot{platform: expected.Platform}
	if payload.Descriptor == nil {
		imageID, err := domain.ParseDigest(payload.ID)
		if err != nil || imageID != expected.ImageConfig {
			return empty, false
		}
		snapshot.imageID = imageID

		return snapshot, true
	}
	manifest, err := domain.ParseDigest(payload.Descriptor.Digest.String())
	imageID, imageIDErr := domain.ParseDigest(payload.ID)
	if err != nil || imageIDErr != nil || imageID != manifest ||
		!validManifestDescriptor(payload.Descriptor.MediaType, payload.Descriptor.Size) {
		return empty, false
	}
	snapshot.imageID = imageID
	snapshot.platformManifest = manifest
	snapshot.hasPlatformManifest = true

	return snapshot, true
}

func validArchiveImageInspect(
	payload imagetypes.InspectResponse,
	expected domain.ImageIdentity,
) bool {
	if payload.Config == nil || payload.Size < 0 {
		return false
	}
	if payload.Os != expected.Platform.OS || payload.Architecture != expected.Platform.Architecture ||
		payload.Variant != expected.Platform.Variant {
		return false
	}
	if !slices.Contains(payload.RepoTags, expected.Reference) {
		return false
	}

	return true
}

func (client *Client) saveArchiveImage(
	ctx context.Context,
	reference string,
	platform domain.Platform,
) (*http.Response, error) {
	path, valid := client.versionedPath("/images/" + reference + "/get")
	if !valid {
		return nil, ErrProtocol
	}
	endpoint := client.baseURL
	endpoint.Path = path
	endpoint.RawQuery = url.Values{
		imagePullPlatformQuery: []string{imagePlatform(platform)},
	}.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, ErrProtocol
	}
	request.Header.Set("Accept", dockerArchiveContentType)

	response, err := client.streamingHTTPClient().Do(request)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("save Docker archive image: %w", ctxErr)
		}

		return nil, ErrUnavailable
	}

	return response, nil
}

func savedArchiveMatches(analysis imagearchive.Analysis, expected domain.ImageIdentity) bool {
	return analysis.MemberIndex == 0 && analysis.SourceReference == expected.Reference &&
		analysis.Identity.Origin == domain.ImageOriginDockerArchive &&
		analysis.Identity.Reference == expected.Reference &&
		analysis.Identity.ReferenceDigest == expected.ReferenceDigest &&
		analysis.Identity.PlatformManifest == expected.PlatformManifest &&
		analysis.Identity.ImageConfig == expected.ImageConfig && analysis.Identity.Platform == expected.Platform
}

func isDockerArchive(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)

	return err == nil && mediaType == dockerArchiveContentType
}

func validArchiveImage(version Version, image domain.ImageIdentity) bool {
	source, err := imageref.Normalize(image.Reference)
	platform, platformValid := dockerPlatform(version.OS, version.Architecture)
	empty := domain.Digest{}

	return image.Origin == domain.ImageOriginDockerArchive && err == nil &&
		source.String() == image.Reference && !source.IsPinned() &&
		platformValid && image.Platform == platform && image.ReferenceDigest != empty &&
		image.PlatformManifest != empty && image.ImageConfig != empty
}
