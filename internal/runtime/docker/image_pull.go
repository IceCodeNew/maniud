package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/moby/moby/api/pkg/authconfig"
	"github.com/moby/moby/api/types/jsonstream"
	registrytypes "github.com/moby/moby/api/types/registry"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
)

const (
	maximumImagePullBytes      = int64(16 << 20)
	maximumImagePullFrameBytes = int64(64 << 10)
	maximumImagePullFrames     = 16 << 10
	maximumImagePullValueBytes = 4 << 10
	maximumImagePullCredential = 16 << 10
	maximumImagePullDuration   = 10 * time.Minute
	dockerRegistryAuthHeader   = "X-Registry-Auth"
	imagePullPlatformQuery     = "platform"
)

// ErrImagePull reports a daemon-side pull failure without exposing registry
// credentials, image references, endpoints, or upstream diagnostics.
var ErrImagePull = errors.New("docker image pull failed")

// RegistryAuthenticator provides one ephemeral Docker registry-auth value.
// Implementations must not persist or log the returned value.
type RegistryAuthenticator interface {
	Credentials(ctx context.Context, reference imageref.Reference) (credential.Value, error)
}

type imagePullMessage struct {
	Stream       string               `json:"stream,omitempty"`
	Status       string               `json:"status,omitempty"`
	ProgressText string               `json:"progress,omitempty"`
	Progress     *jsonstream.Progress `json:"progressDetail,omitempty"` //nolint:tagliatelle // Docker wire field.
	ID           string               `json:"id,omitempty"`
	ErrorText    string               `json:"error,omitempty"`
	Error        *jsonstream.Error    `json:"errorDetail,omitempty"` //nolint:tagliatelle // Docker wire field.
	Aux          *json.RawMessage     `json:"aux,omitempty"`
}

// PullImage asks Docker to pull one immutable reference and platform. A clean,
// bounded response stream does not prove the postcondition; callers must run
// ProbeImage after this method returns.
func (client *Client) PullImage(
	ctx context.Context,
	expected domain.ImageIdentity,
	authenticator RegistryAuthenticator,
) error {
	if client == nil {
		return ErrUnsupportedWorkload
	}

	reference, err := client.imagePullReference(expected)
	if err != nil {
		return err
	}

	pullContext, cancel := context.WithTimeout(ctx, maximumImagePullDuration)
	defer cancel()

	encodedAuth, err := imagePullAuth(pullContext, authenticator, reference)
	if err != nil {
		return err
	}

	request, err := client.newImagePullRequest(pullContext, reference, expected, encodedAuth)
	if err != nil {
		return err
	}

	response, err := client.imagePullHTTPClient().Do(request)
	if err != nil {
		return imagePullRequestError(ctx, pullContext)
	}

	return consumeImagePullResponse(ctx, pullContext, response)
}

func (client *Client) imagePullReference(expected domain.ImageIdentity) (imageref.Reference, error) {
	var empty imageref.Reference

	reference, err := imageref.Parse(expected.Reference)
	if err != nil || !validDockerImage(client.version, expected) {
		return empty, ErrUnsupportedWorkload
	}

	return reference, nil
}

func (client *Client) newImagePullRequest(
	ctx context.Context,
	reference imageref.Reference,
	expected domain.ImageIdentity,
	encodedAuth string,
) (*http.Request, error) {
	path, valid := client.versionedPath("/images/create")
	if !valid {
		return nil, ErrProtocol
	}

	repository := strings.TrimSuffix(
		reference.DigestReference(),
		"@"+expected.ReferenceDigest.String(),
	)

	request, err := client.imagePullRequest(
		ctx,
		path,
		url.Values{
			"fromImage":            {repository},
			imagePullPlatformQuery: {imagePullPlatform(expected.Platform)},
			"tag":                  {expected.ReferenceDigest.String()},
		},
		encodedAuth,
	)
	if err != nil {
		return nil, err
	}

	return request, nil
}

func (client *Client) imagePullRequest(
	ctx context.Context,
	path string,
	query url.Values,
	encodedAuth string,
) (*http.Request, error) {
	endpoint := client.baseURL
	endpoint.Path = path
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), nil)
	if err != nil {
		return nil, ErrProtocol
	}

	request.Header.Set("Accept", jsonContentType)

	if encodedAuth != "" {
		request.Header.Set(dockerRegistryAuthHeader, encodedAuth)
	}

	return request, nil
}

func (client *Client) imagePullHTTPClient() *http.Client {
	pullClient := *client.httpClient
	pullClient.Timeout = 0

	return &pullClient
}

func imagePullAuth(
	ctx context.Context,
	authenticator RegistryAuthenticator,
	reference imageref.Reference,
) (string, error) {
	if authenticator == nil {
		return "", ErrUnavailable
	}

	credentials, err := authenticator.Credentials(ctx, reference)
	if err != nil {
		return "", imagePullRequestError(ctx, ctx)
	}

	if !validImagePullCredentials(credentials) {
		return "", ErrProtocol
	}

	// AuthConfig contains only strings, so encoding/json cannot reject this value.
	encoded, _ := authconfig.Encode(registrytypes.AuthConfig{
		Username:      credentials.Username,
		Password:      credentials.Password,
		Auth:          "",
		ServerAddress: reference.Registry(),
		IdentityToken: credentials.RefreshToken,
		RegistryToken: credentials.AccessToken,
	})

	return encoded, nil
}

func validImagePullCredentials(credentials credential.Value) bool {
	fields := []string{
		credentials.Username,
		credentials.Password,
		credentials.RefreshToken,
		credentials.AccessToken,
	}
	total := 0

	for _, field := range fields {
		if !utf8.ValidString(field) {
			return false
		}

		total += len(field)
	}

	return total <= maximumImagePullCredential
}

func imagePullRequestError(ctx, pullContext context.Context) error {
	ctxErr := ctx.Err()
	if ctxErr != nil {
		return fmt.Errorf("docker image pull: %w", ctxErr)
	}

	pullErr := pullContext.Err()
	if pullErr != nil {
		return fmt.Errorf("docker image pull: %w", pullErr)
	}

	return ErrUnavailable
}

func consumeImagePullResponse(
	ctx context.Context,
	pullContext context.Context,
	response *http.Response,
) error {
	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get("Content-Type")) {
		closeResponse(response)

		return ErrProtocol
	}

	streamErr := decodeImagePullStream(response.Body)
	closeErr := response.Body.Close()

	if streamErr != nil {
		if ctx.Err() != nil || pullContext.Err() != nil {
			return imagePullRequestError(ctx, pullContext)
		}

		return streamErr
	}

	if closeErr != nil {
		return ErrUnavailable
	}

	return nil
}

func imagePullPlatform(platform domain.Platform) string {
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}

	return value
}

func decodeImagePullStream(reader io.Reader) error {
	limited := &io.LimitedReader{R: reader, N: maximumImagePullBytes + 1}
	decoder := json.NewDecoder(limited)

	for frames := 0; ; frames++ {
		raw, done, err := decodeImagePullFrame(decoder, limited, frames)
		if err != nil || done {
			return err
		}

		message, valid := decodeImagePullMessage(raw)
		if !valid {
			return ErrProtocol
		}

		if message.Error != nil {
			return ErrImagePull
		}
	}
}

func decodeImagePullFrame(
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

	if err != nil || frames >= maximumImagePullFrames ||
		int64(len(raw)) == 0 || int64(len(raw)) > maximumImagePullFrameBytes || !utf8.Valid(raw) {
		return nil, false, ErrProtocol
	}

	return raw, false, nil
}

func decodeImagePullMessage(raw json.RawMessage) (imagePullMessage, bool) {
	var message imagePullMessage

	valid := jsonstrict.Decode(bytes.NewReader(raw), maximumImagePullFrameBytes, &message) &&
		validImagePullMessage(message)

	return message, valid
}

func validImagePullMessage(message imagePullMessage) bool {
	values := []string{
		message.Stream,
		message.Status,
		message.ProgressText,
		message.ID,
		message.ErrorText,
	}
	for _, value := range values {
		if !validImagePullValue(value) {
			return false
		}
	}

	if !validImagePullProgress(message.Progress) || !validImagePullError(message) {
		return false
	}

	return hasImagePullContent(message)
}

func hasImagePullContent(message imagePullMessage) bool {
	stringsPresent := message.Stream != "" || message.Status != "" ||
		message.ProgressText != "" || message.ID != ""
	objectsPresent := message.Progress != nil || message.Error != nil || message.Aux != nil

	return stringsPresent || objectsPresent
}

func validImagePullProgress(progress *jsonstream.Progress) bool {
	return progress == nil || progress.Current >= 0 && progress.Total >= 0 && progress.Start >= 0 &&
		(progress.Total == 0 || progress.Current <= progress.Total) &&
		validImagePullValue(progress.Units)
}

func validImagePullError(message imagePullMessage) bool {
	if message.Error == nil {
		return message.ErrorText == ""
	}

	if message.Error.Code < 0 || !validErrorMessage(message.Error.Message) {
		return false
	}

	return message.ErrorText == "" || message.ErrorText == message.Error.Message
}

func validImagePullValue(value string) bool {
	if len(value) > maximumImagePullValueBytes {
		return false
	}

	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}

	return true
}
