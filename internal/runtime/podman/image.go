package podman

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
)

const (
	maximumImagePullBytes      = int64(16 << 20)
	maximumImagePullFrameBytes = int64(64 << 10)
	maximumImagePullFrames     = 16 << 10
	maximumImageCredential     = 16 << 10
	maximumImagePullDuration   = 10 * time.Minute
	registryAuthHeader         = "X-Registry-Auth"
	sha256HexLength            = 64
	pullStatusPulling          = "pulling"
	pullStatusSuccess          = "success"
	pullStatusError            = "error"
)

// ErrImagePull reports a daemon-side pull failure without exposing credentials,
// image references, endpoints, or upstream diagnostics.
var ErrImagePull = errors.New("podman image pull failed")

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

// PullImage asks Libpod to pull one immutable registry reference. Callers must
// run ProbeImage because a successful response stream is not completion proof.
func (client *Client) PullImage(
	ctx context.Context,
	expected domain.ImageIdentity,
	authenticator credential.Provider,
) error {
	reference, valid := client.validImage(expected)
	if !valid || authenticator == nil {
		return ErrUnsupportedWorkload
	}
	pullContext, cancel := context.WithTimeout(ctx, maximumImagePullDuration)
	defer cancel()

	header, err := podmanRegistryAuth(pullContext, authenticator, reference)
	if err != nil {
		return err
	}
	response, err := client.request(
		pullContext,
		http.MethodPost,
		libpodPrefix+"/images/pull",
		url.Values{"reference": {reference.String()}},
		header,
		true,
	)
	if err != nil {
		return imagePullRequestError(ctx, pullContext, err)
	}

	return consumePodmanPullResponse(ctx, pullContext, response)
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

type podmanAuthConfig struct {
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`
	IdentityToken string `json:"identitytoken,omitempty"`
	RegistryToken string `json:"registrytoken,omitempty"`
}

func podmanRegistryAuth(
	ctx context.Context,
	authenticator credential.Provider,
	reference imageref.Reference,
) (http.Header, error) {
	credentials, err := authenticator.Credentials(ctx, reference)
	if err != nil {
		return nil, imagePullRequestError(ctx, ctx, err)
	}
	if !validPodmanCredentials(credentials) {
		return nil, ErrProtocol
	}
	if credentials == (credential.Value{}) {
		return make(http.Header), nil
	}
	// This string-only struct cannot make encoding/json fail; the encoded secret remains request-local.
	payload, _ := json.Marshal(podmanAuthConfig{ //nolint:errchkjson,gosec // See invariant above.
		Username: credentials.Username, Password: credentials.Password,
		IdentityToken: credentials.RefreshToken, RegistryToken: credentials.AccessToken,
	})

	return http.Header{registryAuthHeader: {base64.URLEncoding.EncodeToString(payload)}}, nil
}

func validPodmanCredentials(credentials credential.Value) bool {
	total := 0
	for _, value := range []string{
		credentials.Username, credentials.Password, credentials.RefreshToken, credentials.AccessToken,
	} {
		if !utf8.ValidString(value) {
			return false
		}
		total += len(value)
	}

	return total <= maximumImageCredential
}

type imagePullMessage struct {
	Status   string                   `json:"status,omitempty"`
	Stream   string                   `json:"stream,omitempty"`
	Error    string                   `json:"error,omitempty"`
	Images   []string                 `json:"images,omitempty"`
	ID       string                   `json:"id,omitempty"`
	Progress *podmanImagePullProgress `json:"pullProgress,omitempty"` //nolint:tagliatelle // Libpod wire field.
}

type podmanImagePullProgress struct {
	Status              string `json:"status,omitempty"`
	Current             uint64 `json:"current,omitempty"`
	Total               int64  `json:"total,omitempty"`
	ProgressComponentID string `json:"progressComponentID,omitempty"` //nolint:tagliatelle // Libpod wire field.
}

func consumePodmanPullResponse(
	ctx context.Context,
	pullContext context.Context,
	response *http.Response,
) error {
	if response == nil || response.Body == nil {
		return ErrProtocol
	}
	if response.StatusCode != http.StatusOK || !isPodmanJSON(response.Header.Get(podmanContentType)) {
		_ = response.Body.Close()

		return ErrProtocol
	}
	streamErr := decodePodmanPullStream(response.Body)
	closeErr := response.Body.Close()
	if streamErr != nil {
		if ctx.Err() != nil || pullContext.Err() != nil {
			return imagePullRequestError(ctx, pullContext, streamErr)
		}

		return streamErr
	}
	if closeErr != nil {
		return ErrUnavailable
	}

	return nil
}

func decodePodmanPullStream(reader io.Reader) error {
	limited := &io.LimitedReader{R: reader, N: maximumImagePullBytes + 1}
	decoder := json.NewDecoder(limited)
	state := podmanPullState{}
	for frames := 0; ; frames++ {
		raw, done, err := decodePodmanPullFrame(decoder, limited, frames)
		if err != nil {
			return err
		}
		if done {
			if !state.identity || !state.success {
				return ErrProtocol
			}

			return nil
		}
		message, valid := decodePodmanPullMessage(raw)
		if !valid {
			return ErrProtocol
		}
		state, err = applyPodmanPullMessage(state, message)
		if err != nil {
			return err
		}
	}
}

type podmanPullState struct {
	identity bool
	success  bool
}

func decodePodmanPullFrame(
	decoder *json.Decoder,
	limited *io.LimitedReader,
	frames int,
) (json.RawMessage, bool, error) {
	var raw json.RawMessage
	err := decoder.Decode(&raw)
	if err == io.EOF {
		consumed := maximumImagePullBytes + 1 - limited.N
		if consumed > maximumImagePullBytes {
			return nil, false, ErrProtocol
		}

		return nil, true, nil
	}
	if err != nil || frames >= maximumImagePullFrames || len(raw) == 0 ||
		int64(len(raw)) > maximumImagePullFrameBytes || !utf8.Valid(raw) {
		return nil, false, ErrProtocol
	}

	return raw, false, nil
}

func decodePodmanPullMessage(raw json.RawMessage) (imagePullMessage, bool) {
	var message imagePullMessage
	valid := jsonstrict.Decode(bytes.NewReader(raw), maximumImagePullFrameBytes, &message) &&
		validImagePullMessage(message)

	return message, valid
}

func applyPodmanPullMessage(state podmanPullState, message imagePullMessage) (podmanPullState, error) {
	if message.Error != "" || message.Status == pullStatusError {
		return state, ErrImagePull
	}
	if message.Status == pullStatusSuccess {
		state.success = true
	}
	identifiers := append([]string{message.ID}, message.Images...)
	for _, identifier := range identifiers {
		if identifier == "" {
			continue
		}
		if _, valid := podmanImageID(identifier); !valid {
			return state, ErrProtocol
		}
		state.identity = true
	}

	return state, nil
}

func validImagePullMessage(message imagePullMessage) bool {
	if !validPodmanPullText(message.Stream) || !validPodmanPullText(message.Error) ||
		!validPodmanPullText(message.ID) || len(message.Images) > sha256HexLength {
		return false
	}
	for _, image := range message.Images {
		if !validPodmanPullText(image) {
			return false
		}
	}
	if !validPodmanPullStatus(message.Status) ||
		message.Progress != nil && !validPodmanPullProgress(*message.Progress) {
		return false
	}

	return hasPodmanPullContent(message)
}

func validPodmanPullText(value string) bool {
	return len(value) <= maximumTextBytes && utf8.ValidString(value)
}

func validPodmanPullStatus(value string) bool {
	return value == "" || value == pullStatusPulling || value == pullStatusSuccess || value == pullStatusError
}

func hasPodmanPullContent(message imagePullMessage) bool {
	return message.Stream != "" || message.Error != "" || len(message.Images) > 0 ||
		message.ID != "" || message.Progress != nil || message.Status != ""
}

func validPodmanPullProgress(progress podmanImagePullProgress) bool {
	validStatus := progress.Status == pullStatusPulling || progress.Status == pullStatusSuccess ||
		progress.Status == "skipped"

	return validStatus &&
		progress.Total >= -1 && len(progress.ProgressComponentID) <= maximumTextBytes &&
		utf8.ValidString(progress.ProgressComponentID)
}

func imagePullRequestError(ctx, pullContext context.Context, fallback error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("podman image pull: %w", err)
	}
	if err := pullContext.Err(); err != nil {
		return fmt.Errorf("podman image pull: %w", err)
	}
	if errors.Is(fallback, ErrProtocol) || errors.Is(fallback, ErrInvalidEndpoint) {
		return fallback
	}

	return ErrUnavailable
}
