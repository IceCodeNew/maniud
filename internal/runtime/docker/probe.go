package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/moby/moby/api/types/system"
)

func (client *Client) negotiate(ctx context.Context) (Version, error) {
	var emptyVersion Version

	serverMaximum, err := client.ping(ctx)
	if err != nil {
		return emptyVersion, err
	}

	selected, compatible := compatibleAPIVersion(serverMaximum)
	if !compatible {
		return emptyVersion, ErrProtocol
	}

	return client.serverVersion(ctx, selected, serverMaximum)
}

func (client *Client) ping(ctx context.Context) (apiVersion, error) {
	emptyVersion := apiVersion{major: 0, minor: 0}

	response, err := client.request(ctx, http.MethodHead, "/_ping")
	if err != nil {
		return emptyVersion, err
	}

	if response.StatusCode == http.StatusMethodNotAllowed || response.StatusCode == http.StatusNotFound ||
		response.StatusCode == http.StatusNotImplemented {
		closeResponse(response)

		response, err = client.request(ctx, http.MethodGet, "/_ping")
		if err != nil {
			return emptyVersion, err
		}

		if response.StatusCode != http.StatusOK || !validPingBody(response.Body) {
			closeResponse(response)

			return emptyVersion, ErrProtocol
		}
	} else if response.StatusCode != http.StatusOK {
		closeResponse(response)

		return emptyVersion, ErrProtocol
	}

	header := response.Header.Get(apiVersionHeader)
	closeResponse(response)

	parsed, valid := parseAPIVersion(header)
	if !valid {
		return emptyVersion, ErrProtocol
	}

	return parsed, nil
}

func validPingBody(body io.Reader) bool {
	value, err := io.ReadAll(io.LimitReader(body, maximumPingBytes+1))

	return err == nil && len(value) <= maximumPingBytes && string(bytes.TrimSpace(value)) == "OK"
}

func (client *Client) serverVersion(
	ctx context.Context,
	selected apiVersion,
	pingMaximum apiVersion,
) (Version, error) {
	var emptyVersion Version

	response, err := client.request(ctx, http.MethodGet, "/v"+selected.String()+"/version")
	if err != nil {
		return emptyVersion, err
	}
	defer closeResponse(response)

	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get("Content-Type")) {
		return emptyVersion, ErrProtocol
	}

	var payload system.VersionResponse
	if !decodeStrictJSON(response.Body, &payload) {
		return emptyVersion, ErrProtocol
	}

	serverMinimum, validMinimum := parseAPIVersion(payload.MinAPIVersion)
	serverMaximum, validMaximum := parseAPIVersion(payload.APIVersion)

	if !validServerRange(selected, serverMinimum, validMinimum, serverMaximum, validMaximum, pingMaximum) {
		return emptyVersion, ErrProtocol
	}

	if !completeRuntimeVersion(payload.Version, payload.Os, payload.Arch) {
		return emptyVersion, ErrProtocol
	}

	return Version{
		Protocol:     selected.String(),
		Minimum:      serverMinimum.String(),
		Maximum:      serverMaximum.String(),
		Product:      payload.Version,
		OS:           payload.Os,
		Architecture: payload.Arch,
	}, nil
}

func decodeStrictJSON(reader io.Reader, target any) bool {
	value, err := io.ReadAll(io.LimitReader(reader, maximumJSONBytes+1))
	if err != nil || len(value) > maximumJSONBytes || !uniqueJSONKeys(value) {
		return false
	}

	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()

	if decoder.Decode(target) != nil {
		return false
	}

	return decoder.Decode(&struct{}{}) == io.EOF
}

func uniqueJSONKeys(value []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(value))
	if !consumeJSONValue(decoder) {
		return false
	}

	_, err := decoder.Token()

	return err == io.EOF
}

func consumeJSONValue(decoder *json.Decoder) bool {
	token, err := decoder.Token()
	if err != nil {
		return false
	}

	delimiter, composite := token.(json.Delim)
	if !composite {
		return true
	}

	if delimiter == '[' {
		return consumeJSONArray(decoder)
	}

	return consumeJSONObject(decoder)
}

func consumeJSONArray(decoder *json.Decoder) bool {
	for decoder.More() {
		if !consumeJSONValue(decoder) {
			return false
		}
	}

	return consumeJSONClosing(decoder, ']')
}

func consumeJSONObject(decoder *json.Decoder) bool {
	keys := make(map[string]struct{})

	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return false
		}

		key, _ := keyToken.(string)

		if _, duplicate := keys[key]; duplicate {
			return false
		}

		keys[key] = struct{}{}

		if !consumeJSONValue(decoder) {
			return false
		}
	}

	return consumeJSONClosing(decoder, '}')
}

func consumeJSONClosing(decoder *json.Decoder, expected json.Delim) bool {
	closing, err := decoder.Token()
	if err != nil {
		return false
	}

	closingDelimiter, valid := closing.(json.Delim)

	return valid && closingDelimiter == expected
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)

	return err == nil && mediaType == jsonContentType
}

func closeResponse(response *http.Response) {
	_ = response.Body.Close()
}

func validServerRange(
	selected apiVersion,
	serverMinimum apiVersion,
	validMinimum bool,
	serverMaximum apiVersion,
	validMaximum bool,
	pingMaximum apiVersion,
) bool {
	return validMinimum && validMaximum && serverMaximum == pingMaximum &&
		!selected.Less(serverMinimum) && !serverMaximum.Less(selected)
}

func completeRuntimeVersion(product, operatingSystem, architecture string) bool {
	return product != "" && operatingSystem != "" && architecture != ""
}
