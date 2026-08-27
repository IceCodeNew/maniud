package podman

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/imagerootfs"
)

const (
	podmanDockerArchiveFormat      = "docker-archive"
	maximumPodmanSaveBytes         = int64(1 << 40)
	maximumPodmanSaveProofDuration = 10 * time.Minute
)

// ResolveImageUser resolves a named or incomplete image user against an
// immutable local Podman image export without starting a container.
func (client *Client) ResolveImageUser(
	ctx context.Context,
	expected domain.ImageIdentity,
	specification string,
) (string, error) {
	before, err := client.ProbeImage(ctx, expected)
	if !observedImageMatches(before, expected, err) {
		return "", ErrProtocol
	}

	proofContext, cancel := context.WithTimeout(ctx, maximumPodmanSaveProofDuration)
	defer cancel()
	resolved, err := client.resolveSavedImageUser(proofContext, expected, specification)
	if err != nil {
		return "", err
	}

	after, err := client.ProbeImage(ctx, expected)
	if !observedImageMatches(after, expected, err) || after != before {
		return "", ErrProtocol
	}

	return resolved, nil
}

func (client *Client) resolveSavedImageUser(
	ctx context.Context,
	expected domain.ImageIdentity,
	specification string,
) (string, error) {
	path := client.apiPath(
		"/images/" + strings.TrimPrefix(expected.ImageConfig.String(), "sha256:") + "/get",
	)
	response, err := client.request(
		ctx,
		http.MethodGet,
		path,
		url.Values{"format": []string{podmanDockerArchiveFormat}},
		http.Header{"Accept": []string{podmanArchiveContentType}},
		true,
	)
	if err != nil {
		return "", fmt.Errorf("export Podman image: %w", err)
	}
	if !validPodmanSavedArchive(response) {
		closePodmanResponse(response)

		return "", ErrProtocol
	}

	resolved := ""
	analysis, analyzeErr := imagearchive.AnalyzeStreamVisit(
		ctx,
		response.Body,
		func(visitContext context.Context, path string) error {
			var err error
			resolved, err = imagerootfs.ResolveArchiveUser(visitContext, path, specification)
			if err != nil {
				return fmt.Errorf("resolve Podman image user: %w", err)
			}

			return nil
		},
	)
	closeErr := response.Body.Close()
	if !resolvedPodmanArchiveMatches(response, analysis, expected, analyzeErr, closeErr) {
		return "", ErrProtocol
	}

	return resolved, nil
}

func resolvedPodmanArchiveMatches(
	response *http.Response,
	analysis imagearchive.Analysis,
	expected domain.ImageIdentity,
	analyzeErr error,
	closeErr error,
) bool {
	return analyzeErr == nil && closeErr == nil &&
		(response.ContentLength < 0 || response.ContentLength == analysis.ArchiveSize) &&
		analysis.MemberIndex == 0 && analysis.Identity.ImageConfig == expected.ImageConfig &&
		analysis.Identity.Platform == expected.Platform
}

func observedImageMatches(
	probe application.ImageProbe,
	expected domain.ImageIdentity,
	err error,
) bool {
	return err == nil && probe.State == application.ImageProbeObserved && probe.Matches(expected)
}

func validPodmanSavedArchive(response *http.Response) bool {
	if response == nil || response.Body == nil || response.StatusCode != http.StatusOK ||
		response.ContentLength < -1 || response.ContentLength == 0 ||
		response.ContentLength > maximumPodmanSaveBytes {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get(podmanContentType))

	return err == nil && mediaType == podmanArchiveContentType
}
