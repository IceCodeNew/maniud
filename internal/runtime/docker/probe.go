package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/moby/moby/api/types/system"

	"github.com/IceCodeNew/maniud/internal/jsonstrict"
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

	version, err := client.serverVersion(ctx, selected, serverMaximum)
	if err != nil {
		return emptyVersion, err
	}
	client.protocol = selected

	return version, nil
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

	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get(contentTypeHeader)) {
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
	return jsonstrict.Decode(reader, maximumJSONBytes, target)
}

func decodeCompatibleJSON(
	reader io.Reader,
	target any,
	schema reflect.Type,
	compatibilityFields ...string,
) bool {
	encoded, valid := jsonstrict.Read(reader, maximumJSONBytes)
	if !valid || target == nil || schema.Kind() != reflect.Struct {
		return false
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || fields == nil {
		return false
	}
	for name := range fields {
		if !supportedJSONField(schema, compatibilityFields, name) {
			return false
		}
	}

	return json.Unmarshal(encoded, target) == nil
}

func supportedJSONField(schema reflect.Type, compatibilityFields []string, name string) bool {
	if slices.Contains(compatibilityFields, name) {
		return true
	}

	return supportedSchemaField(schema, name, make(map[reflect.Type]bool))
}

func supportedSchemaField(schema reflect.Type, name string, visited map[reflect.Type]bool) bool {
	schema, valid := concreteStructType(schema)
	if !valid || visited[schema] {
		return false
	}
	visited[schema] = true

	for field := range schema.Fields() {
		if supportedStructField(field, name, visited) {
			return true
		}
	}

	return false
}

func supportedStructField(field reflect.StructField, name string, visited map[reflect.Type]bool) bool {
	fieldName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if fieldName == "-" {
		return false
	}
	if fieldName != "" {
		return fieldName == name
	}
	if !field.Anonymous {
		return field.Name == name
	}

	embedded, valid := concreteStructType(field.Type)
	if valid {
		return supportedSchemaField(embedded, name, visited)
	}

	return field.Name == name
}

func concreteStructType(schema reflect.Type) (reflect.Type, bool) {
	if schema.Kind() == reflect.Pointer {
		schema = schema.Elem()
	}

	return schema, schema.Kind() == reflect.Struct
}

func isJSON(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)

	return err == nil && mediaType == jsonContentType
}

func closeResponse(response *http.Response) {
	if response != nil && response.Body != nil {
		_ = response.Body.Close()
	}
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
