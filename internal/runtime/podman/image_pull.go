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
	"time"
	"unicode/utf8"

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
	pullStatusPulling          = "pulling"
	pullStatusSuccess          = "success"
	pullStatusError            = "error"
)

// ErrImagePull reports a daemon-side pull failure without exposing credentials,
// image references, endpoints, or upstream diagnostics.
var ErrImagePull = errors.New("podman image pull failed")

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
	path := client.apiPath("/images/pull")
	pullContext, cancel := context.WithTimeout(ctx, maximumImagePullDuration)
	defer cancel()

	header, err := podmanRegistryAuth(pullContext, authenticator, reference)
	if err != nil {
		return err
	}
	response, err := client.request(
		pullContext,
		http.MethodPost,
		path,
		url.Values{"reference": {reference.String()}},
		header,
		true,
	)
	if err != nil {
		return imagePullRequestError(ctx, pullContext, err)
	}

	return consumePodmanPullResponse(ctx, pullContext, response)
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
