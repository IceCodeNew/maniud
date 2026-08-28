package main

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/custombuild"
)

const (
	testSourceRoot    = "/source"
	testDockerRuntime = "docker"
	testPodmanRuntime = "podman"
	testTarget        = "linux/arm64"
)

var errBuilderTest = errors.New("builder test failure")

func TestRunBuildsRequestedRuntimeSet(t *testing.T) {
	t.Parallel()

	var got custombuild.Config
	var stdout, stderr bytes.Buffer
	status := run(
		t.Context(),
		[]string{
			"--runtime", testPodmanRuntime, "--runtime", testDockerRuntime,
			"--output", "bin/maniud", "--target", testTarget,
		},
		&stdout,
		&stderr,
		func() (string, error) { return testSourceRoot, nil },
		func(_ context.Context, config custombuild.Config) (custombuild.Manifest, error) {
			got = config

			return custombuild.Manifest{
				Output: testSourceRoot + "/bin/maniud",
				Target: testTarget, Runtimes: []string{testDockerRuntime, testPodmanRuntime},
			}, nil
		},
	)
	if status != 0 || stderr.Len() != 0 || got.Root != testSourceRoot || got.Output != "bin/maniud" ||
		got.Target != testTarget || !slices.Equal(got.Runtimes, []string{testPodmanRuntime, testDockerRuntime}) {
		t.Fatalf("run() = %d, config %#v, stderr %q", status, got, stderr.String())
	}
	var manifest custombuild.Manifest
	if err := json.Unmarshal([]byte(strings.TrimSpace(stdout.String())), &manifest); err != nil ||
		manifest.Output != testSourceRoot+"/bin/maniud" ||
		!slices.Equal(manifest.Runtimes, []string{testDockerRuntime, testPodmanRuntime}) {
		t.Fatalf("run() output = %q, %#v, %v", stdout.String(), manifest, err)
	}
}

func TestRunUsesDefaultBuildSelection(t *testing.T) {
	t.Parallel()

	status := run(
		t.Context(),
		nil,
		io.Discard,
		io.Discard,
		func() (string, error) { return testSourceRoot, nil },
		func(_ context.Context, config custombuild.Config) (custombuild.Manifest, error) {
			if config.Output != defaultOutput || config.Target != "" || config.Runtimes != nil ||
				config.DisableDefaultRuntimes {
				t.Fatalf("default config = %#v", config)
			}

			return custombuild.Manifest{}, nil
		},
	)
	if status != 0 {
		t.Fatalf("run(default) = %d", status)
	}
}

func TestRunDisablesDefaultRuntimeSelection(t *testing.T) {
	t.Parallel()

	status := run(
		t.Context(),
		[]string{"--no-default-runtimes"},
		io.Discard,
		io.Discard,
		func() (string, error) { return testSourceRoot, nil },
		func(_ context.Context, config custombuild.Config) (custombuild.Manifest, error) {
			if !config.DisableDefaultRuntimes || config.Runtimes != nil {
				t.Fatalf("disabled default config = %#v", config)
			}

			return custombuild.Manifest{}, nil
		},
	)
	if status != 0 {
		t.Fatalf("run(no defaults) = %d", status)
	}
}

func TestRunContainsUsageAndBuildFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		getwd      func() (string, error)
		build      buildFunc
		wantStatus int
		wantError  string
	}{
		{name: "help", args: []string{"--help"}, wantStatus: 0, wantError: "Usage: maniud-builder"},
		{name: "unknown flag", args: []string{"--unknown"}, wantStatus: 2, wantError: "flag provided"},
		{name: "positional", args: []string{"extra"}, wantStatus: 2, wantError: "Usage: maniud-builder"},
		{
			name: "working directory", getwd: func() (string, error) { return "", errBuilderTest },
			build: func(context.Context, custombuild.Config) (custombuild.Manifest, error) {
				return custombuild.Manifest{}, nil
			},
			wantStatus: 1, wantError: "resolve source directory",
		},
		{
			name: "build", getwd: func() (string, error) { return testSourceRoot, nil },
			build: func(context.Context, custombuild.Config) (custombuild.Manifest, error) {
				return custombuild.Manifest{}, errBuilderTest
			},
			wantStatus: 1, wantError: errBuilderTest.Error(),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			getwd := test.getwd
			if getwd == nil {
				getwd = func() (string, error) { return testSourceRoot, nil }
			}
			var stderr bytes.Buffer
			status := run(t.Context(), test.args, io.Discard, &stderr, getwd, test.build)
			if status != test.wantStatus || !strings.Contains(stderr.String(), test.wantError) {
				t.Fatalf("run() = %d, stderr %q", status, stderr.String())
			}
		})
	}
}

func TestRunContainsManifestOutputFailures(t *testing.T) {
	t.Parallel()

	var stderr bytes.Buffer
	status := run(
		t.Context(), nil, failingWriter{}, &stderr,
		func() (string, error) { return testSourceRoot, nil },
		func(context.Context, custombuild.Config) (custombuild.Manifest, error) {
			return custombuild.Manifest{Output: "/output"}, nil
		},
	)
	if status != 1 || !strings.Contains(stderr.String(), "write build manifest") {
		t.Fatalf("run(output failure) = %d, stderr %q", status, stderr.String())
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errBuilderTest
}
