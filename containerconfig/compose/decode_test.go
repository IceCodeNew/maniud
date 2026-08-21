//nolint:dupword,goconst,lll // Compose contract matrices keep complete YAML cases readable in place.
package compose

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
)

func TestDecodeSelectsAndNormalizesService(t *testing.T) {
	t.Parallel()

	content := []byte(`
name: example
services:
  ignored:
    profiles: [disabled]
    container_name: ignored
    image: example.test/ignored:1
    network_mode: bridge
  api:
    container_name: example-api
    image: example.test/api:1
    platform: linux/arm64/v8
    network_mode: bridge
    environment:
      B: "2"
      A: ${VALUE}
    volumes:
      - type: bind
        source: /captured/data
        target: /data
`)
	spec, err := Decode(context.Background(), content, DecodeOptions{
		WorkingDirectory: "/captured", Environment: map[string]string{"VALUE": "1"}, Service: "api",
		Paths: PathMapping{From: "/captured", To: "/runtime"},
	})
	wantPlatform := containerconfig.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}
	if err != nil || spec.ServiceName != "api" || spec.Platform != wantPlatform ||
		!reflect.DeepEqual(spec.Environment, []string{"A=1", "B=2"}) ||
		len(spec.Mounts) != 1 || spec.Mounts[0].Source != "/runtime/data" {
		t.Fatalf("Decode() = %#v, %v", spec, err)
	}
	if err := Validate(context.Background(), content, DecodeOptions{
		WorkingDirectory: "/captured", Environment: map[string]string{"VALUE": "1"}, Service: "api",
	}); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestDecodeRejectsUnsafeDocumentsWithFieldPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		path    string
	}{
		{"include", "include: [other.yaml]\nservices: {}\n", "/include"},
		{"environment file", "services:\n  api:\n    env_file: [.env]\n", "/services/api/env_file"},
		{"label file", "services:\n  api:\n    label_file: [labels]\n", "/services/api/label_file"},
		{"extends file", "services:\n  api:\n    extends:\n      file: base.yaml\n      service: base\n", "/services/api/extends/file"},
		{"config file", "configs:\n  cfg:\n    file: cfg\nservices: {}\n", "/configs/cfg/file"},
		{"secret file", "secrets:\n  key:\n    file: key\nservices: {}\n", "/secrets/key/file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := Decode(context.Background(), []byte(test.content), DecodeOptions{WorkingDirectory: "/work"})
			assertValidation(t, err, containerconfig.ValidationUnsupportedField, test.path)
		})
	}
}

func TestDecodeRejectsInvalidDocumentForms(t *testing.T) {
	t.Parallel()

	large := make([]byte, maximumDocumentBytes+1)
	tests := []struct {
		name    string
		content []byte
		options DecodeOptions
	}{
		{"empty", nil, DecodeOptions{WorkingDirectory: "/work"}},
		{"large", large, DecodeOptions{WorkingDirectory: "/work"}},
		{"relative working directory", []byte("services: {}\n"), DecodeOptions{WorkingDirectory: "work"}},
		{"malformed", []byte("services: [\n"), DecodeOptions{WorkingDirectory: "/work"}},
		{"duplicate key", []byte("services: {}\nservices: {}\n"), DecodeOptions{WorkingDirectory: "/work"}},
		{"alias", []byte("services:\n  api: &api {}\n  copy: *api\n"), DecodeOptions{WorkingDirectory: "/work"}},
		{"merge", []byte("services:\n  api:\n    <<: {image: example.test/a}\n"), DecodeOptions{WorkingDirectory: "/work"}},
		{"tag", []byte("services: !custom {}\n"), DecodeOptions{WorkingDirectory: "/work"}},
		{"loader", []byte("services: bad\n"), DecodeOptions{WorkingDirectory: "/work"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := Decode(context.Background(), test.content, test.options)
			assertValidation(t, err, containerconfig.ValidationInvalidDocument, "")
		})
	}
}

func TestDecodeRejectsUnsupportedProjectAndSelection(t *testing.T) {
	t.Parallel()

	baseService := "container_name: api\n    image: example.test/api:1\n    network_mode: bridge\n    platform: linux/amd64\n"
	tests := []struct {
		name    string
		content string
		options DecodeOptions
		code    containerconfig.ValidationCode
		path    string
	}{
		{"network", "networks:\n  default: {}\nservices:\n  api:\n    " + baseService, DecodeOptions{}, containerconfig.ValidationUnsupportedField, "/networks"},
		{"volume", "volumes:\n  data: {}\nservices:\n  api:\n    " + baseService, DecodeOptions{}, containerconfig.ValidationUnsupportedField, "/volumes"},
		{"model", "models:\n  model:\n    model: example\nservices:\n  api:\n    " + baseService, DecodeOptions{}, containerconfig.ValidationUnsupportedField, "/models"},
		{"extension", "x-example: true\nservices:\n  api:\n    " + baseService, DecodeOptions{}, containerconfig.ValidationUnsupportedField, ""},
		{"multiple", "services:\n  api:\n    " + baseService + "  other:\n    container_name: other\n    image: example.test/other:1\n    network_mode: bridge\n    platform: linux/amd64\n", DecodeOptions{}, containerconfig.ValidationInvalidValue, "/services"},
		{"missing", "services:\n  api:\n    " + baseService, DecodeOptions{Service: "missing"}, containerconfig.ValidationInvalidValue, "/services/missing"},
		{"platform mismatch", "services:\n  api:\n    " + baseService, DecodeOptions{Platform: containerconfig.Platform{OS: "linux", Architecture: "arm64"}}, containerconfig.ValidationInvalidValue, "/services/api/platform"},
		{"platform missing", "services:\n  api:\n    container_name: api\n    image: example.test/api:1\n    network_mode: bridge\n", DecodeOptions{}, containerconfig.ValidationInvalidValue, "/services/api/platform"},
		{"platform malformed", "services:\n  api:\n    container_name: api\n    image: example.test/api:1\n    network_mode: bridge\n    platform: linux\n", DecodeOptions{}, containerconfig.ValidationInvalidValue, "/services/api/platform"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			test.options.WorkingDirectory = "/work"
			_, err := Decode(context.Background(), []byte(test.content), test.options)
			assertValidation(t, err, test.code, test.path)
		})
	}
}

func TestDecodePreservesContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Decode(ctx, []byte("services: {}\n"), DecodeOptions{WorkingDirectory: "/work"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode() error = %v", err)
	}
}

func TestDecodePreservesCancellationDuringLoad(t *testing.T) {
	t.Parallel()

	ctx := &cancelAfterFirstCheck{}
	_, err := Decode(ctx, []byte("services: bad\n"), DecodeOptions{WorkingDirectory: "/work"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Decode() error = %v", err)
	}
}

func TestDocumentHelpersAcceptSafeValues(t *testing.T) {
	t.Parallel()

	for _, tag := range []string{
		"!override", "!reset", "!!binary", "!!bool", "!!float", "!!int",
		"!!map", "!!null", "!!seq", "!!str", "!!timestamp",
	} {
		if !approvedYAMLTag(tag) {
			t.Fatalf("approvedYAMLTag() rejected %q", tag)
		}
	}
	if path := externalSourcePath(map[string]any{"services": map[string]any{
		"api": "short", "worker": map[string]any{"extends": map[string]any{"service": "base"}},
	}}); path != "" {
		t.Fatalf("externalSourcePath() = %q", path)
	}
	if name := resourceUsingFile(map[string]any{
		"cfg": map[string]any{"external": true}, "short": "value",
	}); name != "" {
		t.Fatalf("resourceUsingFile() = %q", name)
	}
}

type cancelAfterFirstCheck struct {
	checks atomic.Int32
}

func (*cancelAfterFirstCheck) Deadline() (time.Time, bool) { return time.Time{}, false }

func (*cancelAfterFirstCheck) Done() <-chan struct{} { return nil }

func (*cancelAfterFirstCheck) Value(any) any { return nil }

func (ctx *cancelAfterFirstCheck) Err() error {
	if ctx.checks.Add(1) > 1 {
		return context.Canceled
	}

	return nil
}

func assertValidation(
	t *testing.T,
	err error,
	code containerconfig.ValidationCode,
	path string,
) {
	t.Helper()

	var validation containerconfig.ValidationError
	if !errors.As(err, &validation) || validation.Code != code || validation.Path != path {
		t.Fatalf("validation error = %#v, want code %q path %q", err, code, path)
	}
}
