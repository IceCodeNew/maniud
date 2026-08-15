package compose

import (
	"context"
	"errors"
	"strings"
	"testing"
)

//nolint:funlen // Keeping invalid source spellings together makes the parser contract auditable.
func TestLoadRejectsInvalidSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "whitespace", content: "  \n"},
		{
			name: "duplicate key",
			content: `
services:
  api:
    image: first
    image: second
`,
		},
		{
			name: "unknown service field",
			content: `
services:
  api:
    image: busybox:stable
    unexpected: true
`,
		},
		{
			name: "multiple documents",
			content: `
services:
  api:
    image: busybox:stable
---
services:
  worker:
    image: busybox:stable
`,
		},
		{
			name: "YAML alias",
			content: `
services:
  base: &base
    image: busybox:stable
  api: *base
`,
		},
		{
			name: "YAML merge key",
			content: `
x-base: &base
  image: busybox:stable
services:
  api:
    <<: *base
`,
		},
		{
			name: "custom YAML tag",
			content: `
services:
  api:
    image: !custom busybox:stable
`,
		},
		{name: "root sequence", content: "- services\n"},
		{name: "services sequence", content: "services: [api]\n"},
		{name: "scalar service", content: "services:\n  api: busybox\n"},
		{
			name: "scalar config source",
			content: `
services:
  api:
    image: busybox:stable
configs:
  settings: unexpected
`,
		},
		{
			name: "missing interpolation value",
			content: `
name: ${MISSING:?required}
services:
  api:
    image: busybox:stable
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(context.Background(), testSource(t, test.content))
			if !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("Load() error = %v, want ErrInvalidSource", err)
			}
		})
	}
}

func TestLoadRejectsAmbientEnvironment(t *testing.T) {
	const variable = "MANIUD_COMPOSE_AMBIENT_TEST"

	t.Setenv(variable, "ambient-value")

	source := testSource(t, `
name: ${MANIUD_COMPOSE_AMBIENT_TEST:?required}
services:
  api:
    image: busybox:stable
`)

	_, err := Load(context.Background(), source)
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Load() error = %v, want ErrInvalidSource", err)
	}
}

//nolint:funlen // Keeping external-source spellings together makes the I/O boundary auditable.
func TestLoadRejectsExternalSource(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{
			name: "include",
			content: `
include:
  - missing.yaml
services:
  api:
    image: busybox:stable
`,
		},
		{
			name: "service environment file",
			content: `
services:
  api:
    image: busybox:stable
    env_file: missing.env
`,
		},
		{
			name: "external extends",
			content: `
services:
  api:
    extends:
      file: missing.yaml
      service: base
`,
		},
		{
			name: "service label file",
			content: `
services:
  api:
    image: busybox:stable
    label_file: missing.labels
`,
		},
		{
			name: "config file",
			content: `
services:
  api:
    image: busybox:stable
    configs: [settings]
configs:
  settings:
    file: missing.conf
`,
		},
		{
			name: "secret file",
			content: `
services:
  api:
    image: busybox:stable
    secrets: [credential]
secrets:
  credential:
    file: missing.secret
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := Load(context.Background(), testSource(t, test.content))
			if !errors.Is(err, ErrExternalSource) {
				t.Fatalf("Load() error = %v, want ErrExternalSource", err)
			}
		})
	}
}

func TestLoadRejectsRelativeWorkingDirectory(t *testing.T) {
	t.Parallel()

	source := Source{
		Content: []byte(`
services:
  api:
    image: busybox:stable
`),
		WorkingDir:  ".",
		Environment: nil,
		Profiles:    nil,
	}

	_, err := Load(context.Background(), source)
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Load() error = %v, want ErrInvalidSource", err)
	}
}

func TestLoadRejectsOversizedSource(t *testing.T) {
	t.Parallel()

	source := testSource(t, strings.Repeat("#", maxSourceBytes+1))

	_, err := Load(context.Background(), source)
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Load() error = %v, want ErrInvalidSource", err)
	}
}

func TestLoadHonorsCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Load(ctx, testSource(t, `
services:
  api:
    image: busybox:stable
`))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Load() error = %v, want context.Canceled", err)
	}
}

func TestClassifyLoadErrorPrefersCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := classifyLoadError(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("classifyLoadError() = %v, want context.Canceled", err)
	}
}
