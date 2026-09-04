package compose

import (
	"context"
	"slices"
	"strings"
	"testing"
)

const (
	apiService          = "api"
	debugProfile        = "debug"
	testImageEntrypoint = "/image-entrypoint"
	testImageCommand    = "image-command"
	testInitEntrypoint  = "/init"
	testServeCommand    = "serve"
	testOtherValue      = "other"
	testServicesKey     = "services"
)

func TestLoadUsesExplicitEnvironment(t *testing.T) {
	t.Parallel()

	source := testSource(t, `
name: ${PROJECT_NAME:?set project name}
services:
  api:
    image: busybox:stable
`)
	source.Environment["PROJECT_NAME"] = "example"

	project, err := Load(context.Background(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if project.Name() != "example" {
		t.Fatalf("Name() = %q, want example", project.Name())
	}

	if names := project.ServiceNames(); !slices.Equal(names, []string{apiService}) {
		t.Fatalf("ServiceNames() = %q, want [api]", names)
	}
}

func TestLoadAppliesProfiles(t *testing.T) {
	t.Parallel()

	content := `
name: example
services:
  api:
    image: busybox:stable
  debug:
    image: busybox:stable
    profiles: [debug]
`
	tests := []struct {
		name     string
		profiles []string
		want     []string
	}{
		{name: "default", profiles: nil, want: []string{apiService}},
		{name: debugProfile, profiles: []string{debugProfile}, want: []string{apiService, debugProfile}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := testSource(t, content)
			source.Profiles = test.profiles

			project, err := Load(context.Background(), source)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			if names := project.ServiceNames(); !slices.Equal(names, test.want) {
				t.Fatalf("ServiceNames() = %q, want %q", names, test.want)
			}
		})
	}
}

func TestLoadAllowsSameDocumentExtends(t *testing.T) {
	t.Parallel()

	source := testSource(t, `
name: example
services:
  base:
    image: busybox:stable
  api:
    extends: base
`)

	project, err := Load(context.Background(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if names := project.ServiceNames(); !slices.Equal(names, []string{apiService, "base"}) {
		t.Fatalf("ServiceNames() = %q, want [api base]", names)
	}
}

func TestLoadAllowsComposeOverrideTag(t *testing.T) {
	t.Parallel()

	source := testSource(t, `
name: example
services:
  api:
    image: busybox:stable
    ports: !override
      - "8080:80"
`)

	project, err := Load(context.Background(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if names := project.ServiceNames(); !slices.Equal(names, []string{apiService}) {
		t.Fatalf("ServiceNames() = %q, want [api]", names)
	}
}

func TestApprovedYAMLTags(t *testing.T) {
	t.Parallel()

	for _, tag := range []string{
		"!override", "!reset", "!!binary", "!!bool", "!!float", "!!int",
		"!!map", "!!null", "!!seq", "!!str", "!!timestamp",
	} {
		if !isApprovedYAMLTag(tag) {
			t.Fatalf("isApprovedYAMLTag(%q) = false", tag)
		}
	}

	if isApprovedYAMLTag("!custom") {
		t.Fatal("isApprovedYAMLTag(!custom) = true")
	}
}

func TestResourceWithoutFileStaysInProcess(t *testing.T) {
	t.Parallel()

	resources := map[string]any{
		"settings": map[string]any{"external": true},
	}
	if resourceUsesFile(resources) {
		t.Fatal("resourceUsesFile(external resource) = true")
	}
}

func TestLoadAcceptsMaximumSource(t *testing.T) {
	t.Parallel()

	content := `
name: example
services:
  api:
    image: busybox:stable
`
	content += strings.Repeat("#", maxSourceBytes-len(content))

	_, err := Load(context.Background(), testSource(t, content))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
}

func testSource(t *testing.T, content string) Source {
	t.Helper()

	return Source{
		Content:     []byte(content),
		WorkingDir:  t.TempDir(),
		Environment: make(map[string]string),
		Profiles:    nil,
	}
}
