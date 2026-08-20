package compose

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestZeroProjectRejectsWorkload(t *testing.T) {
	t.Parallel()

	var project Project

	var image domain.ImageIdentity

	_, err := project.Workload("", image)
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Workload() error = %v, want ErrInvalidSource", err)
	}
}

//nolint:funlen // The table is the auditable fail-closed projector boundary.
func TestWorkloadRejectsUnsupportedCoreService(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{
			name: "missing container name",
			content: `
name: example
services:
  api:
    image: example.com/team/api:1
    network_mode: bridge
`,
		},
		{
			name: "uppercase container name",
			content: `
name: example
services:
  api:
    container_name: Example
    image: example.com/team/api:1
    network_mode: bridge
`,
		},
		{
			name: "punctuated container name",
			content: `
name: example
services:
  api:
    container_name: example_api
    image: example.com/team/api:1
    network_mode: bridge
`,
		},
		{
			name: "trailing hyphen container name",
			content: `
name: example
services:
  api:
    container_name: example-
    image: example.com/team/api:1
    network_mode: bridge
`,
		},
		{
			name: "long container name",
			content: `
name: example
services:
  api:
    container_name: ` + strings.Repeat("a", 64) + `
    image: example.com/team/api:1
    network_mode: bridge
`,
		},
		{
			name: "implicit network",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
`,
		},
		{
			name: "host network",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: host
`,
		},
		{
			name: "environment before privacy projection",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    environment:
      TOKEN: private
`,
		},
		{
			name: "published port before port projection",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    ports: ["8080:80"]
`,
		},
		{
			name: "project volume before storage projection",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
volumes:
  data:
`,
		},
		{
			name: "project extension before extension projection",
			content: `
name: example
x-maniud:
  runtime: docker
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`,
		},
		{
			name: "service extension",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    x-extra: true
`,
		},
		{
			name: "uppercase image digest",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: invalid.example/team/api@sha256:` + strings.Repeat("A", 64) + `
    network_mode: bridge
`,
		},
		{
			name: "non-sha256 image digest",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: invalid.example/team/api@sha512:` + strings.Repeat("0", 128) + `
    network_mode: bridge
`,
		},
		{
			name: "image URL scheme",
			content: `
name: example
services:
  api:
    container_name: example-api
    image: https://invalid.example/team/api@` + testReferenceDigest + `
    network_mode: bridge
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			project, err := Load(context.Background(), testSource(t, test.content))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			_, err = project.ImageSource("")
			if !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("Workload() error = %v, want ErrInvalidSource", err)
			}
		})
	}
}

func TestWorkloadAcceptsContainerNameBoundaries(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"a1", "a-b", strings.Repeat("a", 63)} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			workload := loadWorkload(t, `
name: example
services:
  api:
    container_name: `+name+`
    image: example.com/team/api:1
    network_mode: bridge
`)
			if workload.ContainerName != name {
				t.Fatalf("ContainerName = %q, want %q", workload.ContainerName, name)
			}
		})
	}
}
