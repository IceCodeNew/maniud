package cli

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const (
	testRecommendedLocaltimeSource = "source: /etc/localtime"
	testRecommendedRestart         = "restart: unless-stopped"
	testTimezone                   = "Asia/Shanghai"
	testTimezoneAssignment         = "TZ=" + testTimezone
)

type genResolverFixture struct {
	image    domain.ImageIdentity
	err      error
	source   imageref.Source
	platform domain.Platform
}

var errGeneratedComposeTest = errors.New("generated Compose test failure")

type failAfterGenWriter struct {
	writes int
}

func (writer *failAfterGenWriter) Write(value []byte) (int, error) {
	if writer.writes == 0 {
		return 0, errGeneratedComposeTest
	}
	writer.writes--

	return len(value), nil
}

type generatedComposeFailureCase struct {
	name      string
	configure func(*generatedComposeOperations)
}

func observedGeneratedImage(
	_ context.Context,
	_ domain.RuntimeKind,
	expected domain.ImageIdentity,
) (application.ImageProbe, error) {
	return application.ImageProbe{
		State: application.ImageProbeObserved,
		Image: application.ImageEvidence{
			ReferenceDigest:  expected.ReferenceDigest,
			PlatformManifest: expected.PlatformManifest,
			ImageConfig:      expected.ImageConfig,
			Platform:         expected.Platform,
		},
	}, nil
}

func (fixture *genResolverFixture) Resolve(
	_ context.Context,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	fixture.source = source
	fixture.platform = platform
	if fixture.image.Reference == "" && fixture.image.ReferenceDigest != (domain.Digest{}) {
		pinned, err := source.Pin(fixture.image.ReferenceDigest)
		if err != nil {
			return domain.ImageIdentity{}, fmt.Errorf("pin generated image fixture: %w", err)
		}
		fixture.image.Reference = pinned.String()
	}

	return fixture.image, fixture.err
}

func TestExecuteGenWritesResolvedRuntimeCompose(t *testing.T) {
	t.Parallel()

	platform := domain.Platform{OS: linuxOS, Architecture: testArchitectureAMD64}
	resolver := &genResolverFixture{image: generatedImage(t, platform)}
	writtenPath := ""
	writtenContent := []byte(nil)
	output := new(bytes.Buffer)

	err := executeGen(context.Background(), genInvocation{
		runtimeArgs: []string{
			testDockerRuntime, runOperation, "--detach", "--platform=" + testPlatformAMD64,
			testRegistrySource,
		},
		name: testServiceName, output: "generated/service.yaml", json: true,
	}, output, genDependencies{
		workingDirectory:  testWorkingDirectory,
		images:            resolver,
		probeRuntimeImage: observedGeneratedImage,
		write: func(generated generatedCompose) error {
			writtenPath = generated.absolutePath
			writtenContent = append([]byte(nil), generated.content...)

			return nil
		},
	})
	if err != nil {
		t.Fatalf("executeGen() error = %v", err)
	}

	if resolver.source.String() != testRegistrySource || resolver.platform != platform ||
		writtenPath != testWorkingDirectory+"/generated/service.yaml" ||
		!bytes.Contains(writtenContent, []byte("container_name: "+testServiceName)) {
		t.Fatalf("executeGen() = source %q, platform %#v, path %q, content %q",
			resolver.source.String(), resolver.platform, writtenPath, writtenContent)
	}

	wantOutput := `{"path":"generated/service.yaml","status":"generated","warnings":[` +
		`{"code":"runtime_option_ignored","option":"--detach",` +
		`"reason":"the option affects only command execution and was not included in Compose"}]}` + "\n"
	if output.String() != wantOutput {
		t.Fatalf("executeGen() output = %q", output.String())
	}
}

func TestRenderGenRequiresExplicitImageRecommendations(t *testing.T) {
	t.Parallel()

	platform := defaultGeneratedPlatform()
	image := generatedImage(t, platform)
	image.ExposedPorts = []domain.ExposedPort{{TargetPort: 8001, Protocol: protocolTCP}}
	for _, test := range []struct {
		name      string
		arguments genInvocation
		want      bool
	}{
		{name: "direct image defaults disabled", arguments: genInvocation{source: "docker://" + testRegistrySource}},
		{name: "direct image defaults enabled", arguments: genInvocation{
			source: "docker://" + testRegistrySource, recommendedDefaults: true,
		}, want: true},
		{name: "runtime command", arguments: genInvocation{runtimeArgs: []string{
			testDockerRuntime, runOperation, testRegistrySource,
		}}, want: false},
		{name: "runtime command defaults enabled", arguments: genInvocation{
			runtimeArgs:         []string{testDockerRuntime, runOperation, testRegistrySource},
			recommendedDefaults: true,
		}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			generated, err := renderGen(t.Context(), test.arguments, genDependencies{
				workingDirectory:  testWorkingDirectory,
				images:            &genResolverFixture{image: image},
				probeRuntimeImage: observedGeneratedImage,
				recommendations: imageRecommendationOptions{
					localtimeAvailable: true, timezone: testTimezone, timezoneSet: true,
				},
			})
			if err != nil {
				t.Fatalf("renderGen() error = %v", err)
			}
			text := string(generated.content)
			for _, fragment := range []string{
				testRecommendedRestart, "127.0.0.1:8001:8001", "no-new-privileges:true",
				testRecommendedLocaltimeSource, "target: /etc/localtime", "read_only: true", testTimezoneAssignment,
			} {
				if strings.Contains(text, fragment) != test.want {
					t.Fatalf("renderGen() recommendation %q presence = %t, want %t\n%s",
						fragment, strings.Contains(text, fragment), test.want, text)
				}
			}
		})
	}
}

func TestRenderGenRecommendationsPreserveCopiedRuntimeSettings(t *testing.T) {
	t.Parallel()

	platform := defaultGeneratedPlatform()
	image := generatedImage(t, platform)
	image.ExposedPorts = []domain.ExposedPort{
		{TargetPort: 8001, Protocol: protocolTCP},
		{TargetPort: 8002, Protocol: protocolTCP},
	}
	generated, err := renderGen(t.Context(), genInvocation{
		runtimeArgs: []string{
			testDockerRuntime, runOperation,
			"--restart=always", "--env=TZ=UTC", "--publish=0.0.0.0:18001:8001",
			"--volume=/srv/time:/etc/localtime:ro", testRegistrySource,
		},
		recommendedDefaults: true,
	}, genDependencies{
		workingDirectory:  testWorkingDirectory,
		images:            &genResolverFixture{image: image},
		probeRuntimeImage: observedGeneratedImage,
		recommendations: imageRecommendationOptions{
			localtimeAvailable: true, timezone: testTimezone, timezoneSet: true,
		},
	})
	if err != nil {
		t.Fatalf("renderGen() error = %v", err)
	}
	text := string(generated.content)
	for _, fragment := range []string{
		"restart: always", "TZ=UTC", "0.0.0.0:18001:8001", "source: /srv/time", "127.0.0.1:8002:8002",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("renderGen() omitted copied setting %q\n%s", fragment, text)
		}
	}
	for _, fragment := range []string{testRecommendedRestart, testTimezoneAssignment, testRecommendedLocaltimeSource} {
		if strings.Contains(text, fragment) {
			t.Fatalf("renderGen() replaced copied setting with %q\n%s", fragment, text)
		}
	}
}

func TestExecuteGenPrintsShortHumanSummaryAndWarningsByDefault(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	instructions := new(bytes.Buffer)
	err := executeGen(t.Context(), genInvocation{
		source: "docker://" + testRegistrySource,
	}, output, genDependencies{
		workingDirectory:  testWorkingDirectory,
		images:            &genResolverFixture{image: generatedImage(t, defaultGeneratedPlatform())},
		probeRuntimeImage: observedGeneratedImage,
		write:             func(generatedCompose) error { return nil },
		instructions:      instructions,
	})
	if err != nil {
		t.Fatalf("executeGen() error = %v", err)
	}
	if output.String() != "Generated api.yaml.\n" {
		t.Fatalf("executeGen() output = %q", output.String())
	}
	if !strings.Contains(instructions.String(), "add required credentials, URLs, or storage paths") ||
		strings.Contains(instructions.String(), "image_settings_require_review") {
		t.Fatalf("executeGen() instructions = %q", instructions.String())
	}
}

func TestWriteGenResultReportsPreparationImportAndOutputFailures(t *testing.T) {
	t.Parallel()

	generated := generatedCompose{
		path: "service.yaml", preparationPath: "service.prepare.sh",
		importCommand: "docker image load --input image.tar",
	}
	output := new(bytes.Buffer)
	if err := writeGenResult(output, generated, false); err != nil {
		t.Fatalf("writeGenResult() error = %v", err)
	}
	want := "Generated service.yaml.\n" +
		"Preparation script: service.prepare.sh.\n" +
		"Import command: docker image load --input image.tar\n"
	if output.String() != want {
		t.Fatalf("writeGenResult() = %q", output.String())
	}

	tests := []struct {
		name   string
		json   bool
		writes int
	}{
		{name: "JSON", json: true},
		{name: "preparation", writes: 1},
		{name: "import", writes: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			writer := &failAfterGenWriter{writes: test.writes}
			if err := writeGenResult(writer, generated, test.json); !errors.Is(err, errGeneratedComposeTest) {
				t.Fatalf("writeGenResult() error = %v", err)
			}
		})
	}
}

func TestWriteGenWarningsIncludesRuntimeOption(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	writeGenWarnings(output, []runtimeargv.Warning{{Option: "--detach", Reason: "not persisted"}})
	if output.String() != "maniud: warning for --detach: not persisted.\n" {
		t.Fatalf("writeGenWarnings() = %q", output.String())
	}
}

func TestExecuteGenUsesContainerdForNerdctlImages(t *testing.T) {
	t.Parallel()

	platform := domain.Platform{OS: linuxOS, Architecture: testArchitectureAMD64}
	var resolvedSource imageref.Source
	var resolvedPlatform domain.Platform
	output := new(bytes.Buffer)
	err := executeGen(context.Background(), genInvocation{
		runtimeArgs: []string{
			nerdctlRuntimeCommand, createOperation, "--platform=" + testPlatformAMD64, testRegistrySource,
		},
		json: true,
	}, output, genDependencies{
		workingDirectory: testWorkingDirectory,
		runtimeImage: func(
			_ context.Context,
			source imageref.Source,
			selected domain.Platform,
		) (domain.ImageIdentity, error) {
			resolvedSource = source
			resolvedPlatform = selected
			image := generatedImage(t, selected)
			pinned, pinErr := source.Pin(image.ReferenceDigest)
			if pinErr != nil {
				t.Fatal(pinErr)
			}
			image.Reference = pinned.String()

			return image, nil
		},
		write: func(generatedCompose) error { return nil },
	})
	if err != nil {
		t.Fatalf("executeGen() error = %v", err)
	}
	if resolvedSource.String() != testRegistrySource || resolvedPlatform != platform ||
		!strings.Contains(output.String(), `"status":"generated"`) {
		t.Fatalf("executeGen() = source %q, platform %#v, output %q",
			resolvedSource.String(), resolvedPlatform, output.String())
	}
}

func TestResolveGeneratedImageFailures(t *testing.T) {
	t.Parallel()

	projection, err := runtimeargv.Parse(
		[]string{nerdctlRuntimeCommand, createOperation, testRegistrySource}, "", testWorkingDirectory,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveGeneratedImage(context.Background(), projection, genDependencies{})
	if !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("resolveGeneratedImage(missing containerd) = %v", err)
	}
	_, err = resolveGeneratedImage(context.Background(), projection, genDependencies{
		runtimeImage: func(context.Context, imageref.Source, domain.Platform) (domain.ImageIdentity, error) {
			return domain.ImageIdentity{}, registry.ErrProtocol
		},
	})
	if !errors.Is(err, registry.ErrProtocol) {
		t.Fatalf("resolveGeneratedImage(containerd failure) = %v", err)
	}
	_, err = resolveGeneratedImage(context.Background(), projection, genDependencies{
		runtimeImage: func(context.Context, imageref.Source, domain.Platform) (domain.ImageIdentity, error) {
			return domain.ImageIdentity{}, registry.ErrNotFound
		},
	})
	var missing *generatedImageMissingError
	if !errors.As(err, &missing) || !errors.Is(err, registry.ErrNotFound) ||
		missing.Error() != "generated image is unavailable in the selected local runtime" {
		t.Fatalf("resolveGeneratedImage(missing containerd image) = %v", err)
	}
	registryProjection, err := runtimeargv.ParseSource("docker://"+testRegistrySource, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveGeneratedImage(context.Background(), registryProjection, genDependencies{})
	if !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("resolveGeneratedImage(missing registry) = %v", err)
	}
}

//nolint:funlen // Keeping every local image proof state in one table makes fail-closed behavior visible.
func TestResolveGeneratedImageRequiresMatchingLocalRuntimeImage(t *testing.T) {
	t.Parallel()

	projection, err := runtimeargv.ParseSource("docker://"+testRegistrySource, "")
	if err != nil {
		t.Fatal(err)
	}
	image := generatedImage(t, projection.Platform())
	tests := []struct {
		name  string
		probe func(context.Context, domain.RuntimeKind, domain.ImageIdentity) (application.ImageProbe, error)
		want  error
	}{
		{name: "missing probe", want: registry.ErrUnavailable},
		{
			name: "probe error",
			probe: func(context.Context, domain.RuntimeKind, domain.ImageIdentity) (application.ImageProbe, error) {
				return application.ImageProbe{}, registry.ErrUnavailable
			},
			want: registry.ErrUnavailable,
		},
		{
			name: "missing image",
			probe: func(context.Context, domain.RuntimeKind, domain.ImageIdentity) (application.ImageProbe, error) {
				return application.ImageProbe{State: application.ImageProbeMissing}, nil
			},
			want: registry.ErrNotFound,
		},
		{
			name: "mismatched image",
			probe: func(context.Context, domain.RuntimeKind, domain.ImageIdentity) (application.ImageProbe, error) {
				return application.ImageProbe{State: application.ImageProbeObserved}, nil
			},
			want: registry.ErrProtocol,
		},
		{
			name: "unknown image",
			probe: func(context.Context, domain.RuntimeKind, domain.ImageIdentity) (application.ImageProbe, error) {
				return application.ImageProbe{State: application.ImageProbeUnknown}, nil
			},
			want: registry.ErrUnavailable,
		},
		{
			name: "invalid state",
			probe: func(context.Context, domain.RuntimeKind, domain.ImageIdentity) (application.ImageProbe, error) {
				return application.ImageProbe{State: application.ImageProbeState(255)}, nil
			},
			want: registry.ErrProtocol,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := resolveGeneratedImage(t.Context(), projection, genDependencies{
				images: &genResolverFixture{image: image}, probeRuntimeImage: test.probe,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("resolveGeneratedImage() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestProbeGeneratedRuntimeImageClosesRuntime(t *testing.T) {
	t.Parallel()

	image := generatedImage(t, defaultGeneratedPlatform())
	events := []string{}
	probe, err := probeGeneratedRuntimeImage(
		t.Context(), domain.RuntimeDocker, image,
		func(context.Context, domain.RuntimeKind) (applyRuntime, error) {
			return &applyRuntimeFixture{events: &events}, nil
		},
	)
	if err != nil || !probe.Matches(image) || !slices.Equal(events, []string{closeRuntimeEvent}) {
		t.Fatalf("probeGeneratedRuntimeImage() = %#v, %v, events %v", probe, err, events)
	}
	events = nil
	_, err = probeGeneratedRuntimeImage(
		t.Context(), domain.RuntimeDocker, image,
		func(context.Context, domain.RuntimeKind) (applyRuntime, error) {
			return &applyRuntimeFixture{events: &events, probeErr: errGeneratedComposeTest}, nil
		},
	)
	if !errors.Is(err, errGeneratedComposeTest) || !slices.Equal(events, []string{closeRuntimeEvent}) {
		t.Fatalf("probeGeneratedRuntimeImage(probe error) = %v, events %v", err, events)
	}
}

type imageUserRuntimeFixture struct {
	*applyRuntimeFixture

	resolved string
	err      error
}

func (runtime *imageUserRuntimeFixture) ResolveImageUser(
	context.Context,
	domain.ImageIdentity,
	string,
) (string, error) {
	return runtime.resolved, runtime.err
}

func TestResolveGeneratedImageUserClosesRuntime(t *testing.T) {
	t.Parallel()

	image := generatedImage(t, defaultGeneratedPlatform())
	tests := []struct {
		name      string
		open      func(*[]string) (applyRuntime, error)
		want      string
		wantErr   error
		wantClose bool
	}{
		{
			name:    "open failure",
			open:    func(*[]string) (applyRuntime, error) { return nil, errGeneratedComposeTest },
			wantErr: errGeneratedComposeTest,
		},
		{
			name: "unsupported runtime",
			open: func(events *[]string) (applyRuntime, error) {
				return &applyRuntimeFixture{events: events}, nil
			},
			wantErr: registry.ErrUnavailable, wantClose: true,
		},
		{
			name: "resolver failure",
			open: func(events *[]string) (applyRuntime, error) {
				return &imageUserRuntimeFixture{
					applyRuntimeFixture: &applyRuntimeFixture{events: events}, err: errGeneratedComposeTest,
				}, nil
			},
			wantErr: errGeneratedComposeTest, wantClose: true,
		},
		{
			name: "resolved",
			open: func(events *[]string) (applyRuntime, error) {
				return &imageUserRuntimeFixture{
					applyRuntimeFixture: &applyRuntimeFixture{events: events}, resolved: testNumericUser,
				}, nil
			},
			want: testNumericUser, wantClose: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := []string{}
			got, err := resolveGeneratedImageUser(
				t.Context(), domain.RuntimeDocker, image, testServiceName,
				func(context.Context, domain.RuntimeKind) (applyRuntime, error) { return test.open(&events) },
			)
			if got != test.want || !errors.Is(err, test.wantErr) ||
				test.wantClose != slices.Equal(events, []string{closeRuntimeEvent}) {
				t.Fatalf("resolveGeneratedImageUser() = %q, %v, events %v", got, err, events)
			}
		})
	}
}

func TestWriteGenFailureHint(t *testing.T) {
	t.Parallel()

	source, err := imageref.Normalize(testRegistrySource)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "Docker image",
			err:  &generatedImageMissingError{runtime: domain.RuntimeDocker, source: source},
			want: "'docker pull " + testRegistrySource + "'",
		},
		{
			name: "containerd image",
			err:  &generatedImageMissingError{runtime: domain.RuntimeContainerd, source: source},
			want: "'nerdctl pull " + testRegistrySource + "'",
		},
		{
			name: "numeric owner",
			err:  fmt.Errorf("wrapped: %w", &bindPreparationOwnerError{}),
			want: "does not contain a resolvable account",
		},
		{name: "unrelated", err: errGeneratedComposeTest, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			output := new(bytes.Buffer)
			writeGenFailureHint(output, test.err)
			if test.want == "" && output.Len() != 0 {
				t.Fatalf("writeGenFailureHint() = %q, want no output", output.String())
			}
			if test.want != "" && !strings.Contains(output.String(), test.want) {
				t.Fatalf("writeGenFailureHint() = %q, want %q", output.String(), test.want)
			}
		})
	}
	writeGenFailureHint(nil, &generatedImageMissingError{runtime: domain.RuntimeDocker, source: source})
}

func TestExecuteGenAnalyzesArchiveWithoutImporting(t *testing.T) {
	t.Parallel()

	archivePath := writeGenArchive(t)
	writtenPath := ""
	writtenContent := []byte(nil)
	output := new(bytes.Buffer)
	err := executeGen(context.Background(), genInvocation{
		source: "docker-archive:" + archivePath + ":example.com/team/app:1", json: true,
		recommendedDefaults: true,
	}, output, genDependencies{
		workingDirectory: testWorkingDirectory,
		analyzeArchive:   imagearchive.Analyze,
		recommendations:  imageRecommendationOptions{localtimeAvailable: true},
		write: func(generated generatedCompose) error {
			writtenPath = generated.absolutePath
			writtenContent = bytes.Clone(generated.content)

			return nil
		},
	})
	if err != nil {
		t.Fatalf("executeGen() error = %v", err)
	}
	assertGeneratedArchiveContent(t, writtenPath, writtenContent, archivePath)
	if !strings.Contains(output.String(), `"import_command":"docker image load --input`) ||
		!strings.Contains(output.String(), `"status":"generated"`) {
		t.Fatalf("executeGen() output = %q", output.String())
	}
}

func assertGeneratedArchiveContent(t *testing.T, path string, content []byte, archivePath string) {
	t.Helper()

	if path != testWorkingDirectory+"/app.yaml" || bytes.Contains(content, []byte(archivePath)) {
		t.Fatalf("executeGen() = path %q, content %q", path, content)
	}
	for _, fragment := range []string{
		"kind: docker-archive",
		testRecommendedRestart,
		"127.0.0.1:8080:8080",
		"no-new-privileges:true",
		testRecommendedLocaltimeSource,
	} {
		if !bytes.Contains(content, []byte(fragment)) {
			t.Fatalf("executeGen() content = %q, missing %q", content, fragment)
		}
	}
}

func TestExecuteGenContainsArchiveFailures(t *testing.T) {
	t.Parallel()

	archivePath := writeGenArchive(t)
	tests := []struct {
		name    string
		source  string
		analyze func(context.Context, imagearchive.Source) (imagearchive.Analysis, error)
		want    error
	}{
		{name: "invalid source", source: "docker-archive:relative@0", want: imagearchive.ErrInvalidSource},
		{name: "missing analyzer", source: "docker-archive:" + archivePath + "@0", want: runtimeargv.ErrInvalid},
		{
			name: "analysis failure", source: "docker-archive:" + archivePath + "@0",
			analyze: func(context.Context, imagearchive.Source) (imagearchive.Analysis, error) {
				return imagearchive.Analysis{}, imagearchive.ErrInvalidArchive
			},
			want: imagearchive.ErrInvalidArchive,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := executeGen(context.Background(), genInvocation{source: test.source}, io.Discard, genDependencies{
				workingDirectory: testWorkingDirectory,
				analyzeArchive:   test.analyze,
				write:            func(generatedCompose) error { return nil },
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("executeGen() error = %v, want %v", err, test.want)
			}
		})
	}
}

func writeGenArchive(t *testing.T) string {
	t.Helper()

	layer := []byte("layer")
	config := fmt.Appendf(nil,
		`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[%q]},`+
			`"config":{"Entrypoint":["/init"],"Cmd":["serve"],"ExposedPorts":{"8080/tcp":{}}}}`,
		domain.Hash(layer).String(),
	)
	manifest := []byte(`[{"Config":"config.json","RepoTags":["example.com/team/app:1"],` +
		`"Layers":["layer.tar"]}]`)
	var buffer bytes.Buffer
	archive := tar.NewWriter(&buffer)
	for _, member := range []struct {
		name string
		body []byte
	}{
		{name: "manifest.json", body: manifest},
		{name: "config.json", body: config},
		{name: "layer.tar", body: layer},
	} {
		if err := archive.WriteHeader(&tar.Header{
			Name: member.name, Mode: 0o600, Size: int64(len(member.body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("WriteHeader() error = %v", err)
		}
		if _, err := archive.Write(member.body); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	path := filepath.Join(t.TempDir(), "image.tar")
	if err := os.WriteFile(path, buffer.Bytes(), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	return path
}

func TestExecuteGenUsesDefaultOutputAndContainsFailures(t *testing.T) {
	t.Parallel()

	platform := defaultGeneratedPlatform()
	valid := genInvocation{runtimeArgs: []string{testDockerRuntime, "create", testRegistrySource}}

	for _, test := range genFailureCases(t, platform, valid) {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			writePath := ""
			write := test.write
			if write == nil {
				write = func(generated generatedCompose) error {
					writePath = generated.absolutePath

					return nil
				}
			}

			err := executeGen(context.Background(), test.arguments, test.output, genDependencies{
				workingDirectory:  testWorkingDirectory,
				images:            test.resolver,
				probeRuntimeImage: observedGeneratedImage,
				write:             write,
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("executeGen() error = %v, want %v", err, test.want)
			}
			if test.wantWritePath != "" && writePath != test.wantWritePath {
				t.Fatalf("executeGen() write path = %q", writePath)
			}
		})
	}
}

type genFailureCase struct {
	name          string
	arguments     genInvocation
	resolver      *genResolverFixture
	output        io.Writer
	write         func(generatedCompose) error
	want          error
	wantWritePath string
}

func genFailureCases(t *testing.T, platform domain.Platform, valid genInvocation) []genFailureCase {
	t.Helper()
	mismatchedPlatform := domain.Platform{OS: linuxOS, Architecture: testArchitectureAMD64}
	if platform.Architecture == testArchitectureAMD64 {
		mismatchedPlatform = domain.Platform{OS: linuxOS, Architecture: "arm64", Variant: "v8"}
	}

	return []genFailureCase{
		{
			name:          "source mode",
			arguments:     genInvocation{source: "docker://" + testImageSource},
			resolver:      &genResolverFixture{image: generatedImage(t, platform)},
			output:        io.Discard,
			wantWritePath: testWorkingDirectory + "/image.yaml",
		},
		{
			name:      "invalid argv",
			arguments: genInvocation{runtimeArgs: []string{testDockerRuntime}},
			resolver:  &genResolverFixture{}, output: io.Discard, want: runtimeargv.ErrInvalid,
		},
		{
			name: "unsafe bind source",
			arguments: genInvocation{runtimeArgs: []string{
				testDockerRuntime, createOperation, "--volume=/:/data", testRegistrySource,
			}},
			resolver: &genResolverFixture{image: generatedImage(t, platform)},
			output:   io.Discard, want: runtimeargv.ErrInvalid,
		},
		{
			name: "resolve failure", arguments: valid,
			resolver: &genResolverFixture{err: registry.ErrUnavailable},
			output:   io.Discard, want: registry.ErrUnavailable,
		},
		{
			name: "identity mismatch", arguments: valid,
			resolver: &genResolverFixture{image: generatedImage(t, mismatchedPlatform)},
			output:   io.Discard, want: runtimeargv.ErrInvalid,
		},
		{
			name: "write failure", arguments: valid,
			resolver: &genResolverFixture{image: generatedImage(t, platform)}, output: io.Discard,
			write: func(generatedCompose) error { return io.ErrClosedPipe }, want: io.ErrClosedPipe,
		},
		{
			name: "output failure", arguments: valid,
			resolver: &genResolverFixture{image: generatedImage(t, platform)},
			output:   failingWriter{}, want: errClosedOutput,
			wantWritePath: testWorkingDirectory + "/api.yaml",
		},
	}
}

func TestDefaultGenDependencies(t *testing.T) {
	t.Parallel()

	dependencies, err := defaultGenDependencies(map[string]string{}, io.Discard, func() (string, error) {
		return testWorkingDirectory, nil
	}, testRuntimePlugins(t))
	assertDefaultGenDependencies(t, dependencies, err)
	source, err := imageref.Normalize(testRegistrySource)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dependencies.runtimeImage(
		context.Background(), source, defaultGeneratedPlatform(),
	); !errors.Is(err, registry.ErrProtocol) {
		t.Fatalf("runtimeImage(unconfigured) = %v", err)
	}
	if _, err := dependencies.probeRuntimeImage(
		context.Background(), domain.RuntimeKind("unsupported"), generatedImage(t, defaultGeneratedPlatform()),
	); !errors.Is(err, application.ErrInvalidRequest) {
		t.Fatalf("probeRuntimeImage(unsupported) = %v", err)
	}
	if _, err := dependencies.resolveImageUser(
		context.Background(), domain.RuntimeKind("unsupported"),
		generatedImage(t, defaultGeneratedPlatform()), testServiceName,
	); !errors.Is(err, application.ErrInvalidRequest) {
		t.Fatalf("resolveImageUser(unsupported) = %v", err)
	}

	for _, getWorkingDirectory := range []func() (string, error){
		func() (string, error) { return "", io.ErrClosedPipe },
		func() (string, error) { return testRelativePath, nil },
	} {
		if _, err := defaultGenDependencies(
			nil, io.Discard, getWorkingDirectory, testRuntimePlugins(t),
		); err == nil {
			t.Fatal("defaultGenDependencies(invalid cwd) succeeded")
		}
	}
}

func assertDefaultGenDependencies(t *testing.T, dependencies genDependencies, err error) {
	t.Helper()

	if err != nil || dependencies.workingDirectory != testWorkingDirectory ||
		dependencies.images == nil || dependencies.runtimeImage == nil || dependencies.probeRuntimeImage == nil ||
		dependencies.resolveImageUser == nil || dependencies.analyzeArchive == nil ||
		dependencies.recommendations.publishPorts == nil || dependencies.write == nil {
		t.Fatalf("defaultGenDependencies() = %#v, %v", dependencies, err)
	}
}

func TestWriteGeneratedComposeCreatesPrivateNoClobberFile(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "service.yaml")
	content := []byte("services: {}\n")

	if err := writeGeneratedCompose(path, content); err != nil {
		t.Fatalf("writeGeneratedCompose() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("generated file = %#v, %v", info, err)
	}
	got, err := os.ReadFile(path) //nolint:gosec // path is created under t.TempDir above.
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("generated content = %q, %v", got, err)
	}

	if err := writeGeneratedCompose(path, []byte("changed")); err == nil {
		t.Fatal("writeGeneratedCompose(existing) succeeded")
	}
	got, _ = os.ReadFile(path) //nolint:gosec // path is created under t.TempDir above.
	if !bytes.Equal(got, content) {
		t.Fatalf("existing content changed to %q", got)
	}

	if err := writeGeneratedCompose(filepath.Join(directory, "missing", "service.yaml"), content); err == nil {
		t.Fatal("writeGeneratedCompose(missing parent) succeeded")
	}
}

func TestWriteGeneratedFilesPublishesOrRollsBackTheWholeBundle(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	composePath := filepath.Join(directory, "service.yaml")
	preparationPath := filepath.Join(directory, "service.prepare.sh")
	generated := generatedCompose{
		absolutePath: composePath, content: []byte("services: {}\n"),
		preparationAbsolute: preparationPath, preparation: []byte("#!/bin/sh\n"),
	}
	if err := writeGeneratedFiles(generated); err != nil {
		t.Fatalf("writeGeneratedFiles() error = %v", err)
	}
	for path, want := range map[string][]byte{
		composePath: []byte("services: {}\n"), preparationPath: []byte("#!/bin/sh\n"),
	} {
		content, err := os.ReadFile(path) //nolint:gosec // Test-owned temporary path.
		if err != nil || !bytes.Equal(content, want) {
			t.Fatalf("generated artifact %q = %q, %v", path, content, err)
		}
	}

	rollbackDirectory := t.TempDir()
	composePath = filepath.Join(rollbackDirectory, "service.yaml")
	preparationPath = filepath.Join(rollbackDirectory, "service.prepare.sh")
	if err := os.WriteFile(preparationPath, []byte("keep\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	generated.absolutePath = composePath
	generated.preparationAbsolute = preparationPath
	if err := writeGeneratedFiles(generated); err == nil {
		t.Fatal("writeGeneratedFiles(existing sibling) succeeded")
	}
	if _, err := os.Stat(composePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back Compose stat = %v", err)
	}
	content, err := os.ReadFile(preparationPath) //nolint:gosec // Test-owned temporary path.
	if err != nil || string(content) != "keep\n" {
		t.Fatalf("existing preparation script = %q, %v", content, err)
	}
}

func TestWriteGeneratedFilesPublishesComposeWithoutPreparation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "service.yaml")
	if err := writeGeneratedFiles(generatedCompose{
		absolutePath: path, content: []byte("services: {}\n"),
	}); err != nil {
		t.Fatalf("writeGeneratedFiles(Compose only) error = %v", err)
	}
}

func TestGeneratedComposeOwnershipChecks(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "generated.yaml")
	testGeneratedComposeIdentityChanges(t, path)
	testGeneratedComposeLifecycle(t, path)
}

func TestOpenGeneratedComposeFailureBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*generatedComposeOperations)
	}{
		{
			name: "root",
			configure: func(operations *generatedComposeOperations) {
				operations.openRoot = func(string) (*os.Root, error) { return nil, errGeneratedComposeTest }
			},
		},
		{
			name: "directory",
			configure: func(operations *generatedComposeOperations) {
				operations.openDirectory = func(*os.Root) (*os.File, error) { return nil, errGeneratedComposeTest }
			},
		},
		{
			name: "file",
			configure: func(operations *generatedComposeOperations) {
				operations.openFile = func(*os.Root, string) (*os.File, error) { return nil, errGeneratedComposeTest }
			},
		},
		{
			name: "identity",
			configure: func(operations *generatedComposeOperations) {
				operations.statFile = func(*os.File) (os.FileInfo, error) { return nil, errGeneratedComposeTest }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operations := generatedComposeDefaultOperations()
			test.configure(&operations)
			path := filepath.Join(t.TempDir(), "generated.yaml")

			owned, err := openGeneratedComposeWithOperations(path, operations)
			if owned != nil || !errors.Is(err, errGeneratedComposeTest) {
				t.Fatalf("openGeneratedComposeWithOperations() = %#v, %v", owned, err)
			}
		})
	}
}

func TestWriteGeneratedComposeEarlyFailureBoundaries(t *testing.T) {
	t.Parallel()

	testWriteGeneratedComposeFailures(t, []generatedComposeFailureCase{
		{
			name: "mode",
			configure: func(operations *generatedComposeOperations) {
				operations.chmodFile = func(*os.File, os.FileMode) error { return errGeneratedComposeTest }
			},
		},
		{
			name: "content output",
			configure: func(operations *generatedComposeOperations) {
				operations.writeFile = func(*os.File, []byte) (int, error) { return 0, errGeneratedComposeTest }
			},
		},
		{
			name: "short write",
			configure: func(operations *generatedComposeOperations) {
				operations.writeFile = func(*os.File, []byte) (int, error) { return 0, nil }
			},
		},
		{
			name: "file sync",
			configure: func(operations *generatedComposeOperations) {
				operations.syncFile = func(*os.File) error { return errGeneratedComposeTest }
			},
		},
		{
			name: "first identity check",
			configure: func(operations *generatedComposeOperations) {
				operations.lstat = func(*os.Root, string) (os.FileInfo, error) {
					return nil, errGeneratedComposeTest
				}
			},
		},
	})
}

func TestWriteGeneratedComposePublishFailureBoundaries(t *testing.T) {
	t.Parallel()

	testWriteGeneratedComposeFailures(t, []generatedComposeFailureCase{
		{
			name: "file close",
			configure: func(operations *generatedComposeOperations) {
				operations.closeFile = func(file *os.File) error {
					return errors.Join(file.Close(), errGeneratedComposeTest)
				}
			},
		},
		{
			name: "directory sync",
			configure: func(operations *generatedComposeOperations) {
				operations.syncDirectory = func(*os.File) error { return errGeneratedComposeTest }
			},
		},
		{
			name: "second identity check",
			configure: func(operations *generatedComposeOperations) {
				calls := 0
				operations.lstat = func(root *os.Root, name string) (os.FileInfo, error) {
					calls++
					if calls == 2 {
						return nil, errGeneratedComposeTest
					}

					return generatedComposeDefaultOperations().lstat(root, name)
				}
			},
		},
	})
}

func testWriteGeneratedComposeFailures(t *testing.T, tests []generatedComposeFailureCase) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operations := generatedComposeDefaultOperations()
			test.configure(&operations)
			path := filepath.Join(t.TempDir(), "generated.yaml")

			err := writeGeneratedComposeWithOperations(path, []byte("services: {}\n"), operations)
			if err == nil {
				t.Fatal("writeGeneratedComposeWithOperations() succeeded")
			}
		})
	}
}

func TestRemoveGeneratedComposeFailureBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*generatedComposeOperations)
	}{
		{
			name: "inspect",
			configure: func(operations *generatedComposeOperations) {
				operations.lstat = func(*os.Root, string) (os.FileInfo, error) {
					return nil, errGeneratedComposeTest
				}
			},
		},
		{
			name: "remove",
			configure: func(operations *generatedComposeOperations) {
				operations.remove = func(*os.Root, string) error { return errGeneratedComposeTest }
			},
		},
		{
			name: "directory sync",
			configure: func(operations *generatedComposeOperations) {
				operations.syncDirectory = func(*os.File) error { return errGeneratedComposeTest }
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "generated.yaml")
			owned, err := openGeneratedCompose(path)
			if err != nil {
				t.Fatalf("openGeneratedCompose() error = %v", err)
			}
			operations := generatedComposeDefaultOperations()
			test.configure(&operations)
			owned.operations = operations

			if err := owned.remove(); err == nil {
				t.Fatal("remove() succeeded")
			}
			owned.operations = generatedComposeDefaultOperations()
			_ = owned.remove()
			_ = owned.close()
		})
	}
}

func testGeneratedComposeIdentityChanges(t *testing.T, path string) {
	t.Helper()

	owned, err := openGeneratedCompose(path)
	if err != nil {
		t.Fatalf("openGeneratedCompose() error = %v", err)
	}
	if err := owned.file.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	owned.file = nil
	if err := owned.revalidate(0); err != nil {
		t.Fatalf("revalidate() error = %v", err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := owned.revalidate(0); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("revalidate(changed) error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	if err := owned.revalidate(0); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("revalidate(missing) error = %v", err)
	}
	if err := owned.remove(); err != nil {
		t.Fatalf("remove(missing) error = %v", err)
	}
	if err := owned.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
}

func testGeneratedComposeLifecycle(t *testing.T, path string) {
	t.Helper()

	owned, err := openGeneratedCompose(path)
	if err != nil {
		t.Fatalf("openGeneratedCompose(second) error = %v", err)
	}
	if err := owned.remove(); err != nil {
		t.Fatalf("remove() error = %v", err)
	}
	if err := owned.close(); err != nil {
		t.Fatalf("close(second) error = %v", err)
	}
}

func TestGeneratedComposeRemoveRejectsReplacement(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "generated.yaml")
	owned, err := openGeneratedCompose(path)
	if err != nil {
		t.Fatalf("openGeneratedCompose() error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove original generated Compose: %v", err)
	}
	if err := os.WriteFile(path, []byte("replacement"), generatedFileMode); err != nil {
		t.Fatalf("write replacement generated Compose: %v", err)
	}
	if err := owned.remove(); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("remove(replacement) error = %v", err)
	}
	if err := owned.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
}

func TestGenRejectsAmbiguousProjectionAndInvalidArchiveName(t *testing.T) {
	t.Parallel()

	_, err := parseGenProjection(genInvocation{}, testWorkingDirectory)
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("parseGenProjection(empty) error = %v", err)
	}
	_, err = parseGenProjection(
		genInvocation{source: "docker://" + testImageSource, runtimeArgs: []string{testDockerRuntime}},
		testWorkingDirectory,
	)
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("parseGenProjection(ambiguous) error = %v", err)
	}
	_, err = parseGenProjection(genInvocation{source: "docker://\x00"}, testWorkingDirectory)
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("parseGenProjection(invalid source) error = %v", err)
	}

	archivePath := writeGenArchive(t)
	rendered, name, command, warnings, err := renderArchiveGen(context.Background(), genInvocation{
		source: "docker-archive:" + archivePath + "@0", name: "bad name",
	}, genDependencies{analyzeArchive: imagearchive.Analyze})
	if err == nil {
		t.Fatalf("renderArchiveGen(invalid name) = %q, %q, %q, %#v", rendered, name, command, warnings)
	}
}

func TestRenderGenContainsRecommendationFailures(t *testing.T) {
	t.Parallel()

	platform := defaultGeneratedPlatform()
	image := generatedImage(t, platform)
	image.ExposedPorts = []domain.ExposedPort{{TargetPort: 8080, Protocol: protocolTCP}}
	failingPublisher := func([]domain.ExposedPort) ([]domain.PortBinding, error) {
		return nil, errGeneratedComposeTest
	}
	if _, err := renderGen(t.Context(), genInvocation{
		source: "docker://" + testRegistrySource, recommendedDefaults: true,
	}, genDependencies{
		workingDirectory:  testWorkingDirectory,
		images:            &genResolverFixture{image: image},
		probeRuntimeImage: observedGeneratedImage,
		recommendations:   imageRecommendationOptions{publishPorts: failingPublisher, localtimeAvailable: true},
	}); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("renderGen(recommendation failure) error = %v", err)
	}

	archivePath := writeGenArchive(t)
	source, err := imagearchive.ParseSource("docker-archive:" + archivePath + "@0")
	if err != nil {
		t.Fatal(err)
	}
	analysis, err := imagearchive.Analyze(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	analyze := func(context.Context, imagearchive.Source) (imagearchive.Analysis, error) {
		return analysis, nil
	}
	arguments := genInvocation{source: "docker-archive:" + archivePath + "@0", recommendedDefaults: true}
	if _, _, _, _, err := renderArchiveGen(t.Context(), arguments, genDependencies{
		workingDirectory: testWorkingDirectory, analyzeArchive: analyze,
		recommendations: imageRecommendationOptions{publishPorts: failingPublisher, localtimeAvailable: true},
	}); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("renderArchiveGen(recommendation failure) error = %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, _, _, err := renderArchiveGen(ctx, arguments, genDependencies{
		workingDirectory: testWorkingDirectory, analyzeArchive: analyze,
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("renderArchiveGen(canceled render) error = %v", err)
	}
}

func TestRenderGenRejectsInvalidOutputPath(t *testing.T) {
	t.Parallel()

	archivePath := writeGenArchive(t)
	_, err := renderGen(context.Background(), genInvocation{
		source: "docker-archive:" + archivePath + "@0",
		output: invalidPathValue,
	}, genDependencies{
		workingDirectory: testWorkingDirectory,
		analyzeArchive:   imagearchive.Analyze,
	})
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("renderGen(archive invalid output) error = %v", err)
	}

	_, err = renderGen(context.Background(), genInvocation{
		source: "docker://" + testImageSource,
		output: invalidPathValue,
	}, genDependencies{workingDirectory: testWorkingDirectory})
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("renderGen(registry invalid output) error = %v", err)
	}

	_, _, err = generatedComposePath(invalidPathValue, testServiceName, testWorkingDirectory)
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("generatedComposePath(invalid output) error = %v", err)
	}

	absolute := filepath.Join(testWorkingDirectory, "compose.yaml")
	selected, resolved, err := generatedComposePath(absolute, testServiceName, testWorkingDirectory)
	if err != nil || selected != absolute || resolved != absolute {
		t.Fatalf("generatedComposePath(absolute) = %q, %q, %v", selected, resolved, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = renderGen(ctx, genInvocation{
		source: "docker://" + testImageSource,
	}, genDependencies{
		workingDirectory: testWorkingDirectory,
		images: &genResolverFixture{
			image: generatedImage(t, defaultGeneratedPlatform()),
		},
		probeRuntimeImage: observedGeneratedImage,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("renderGen(cancelled) error = %v", err)
	}
}

func TestRunGenAndFailureClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		execute func(genInvocation) error
		want    string
	}{
		{name: "success", execute: func(genInvocation) error { return nil }, want: ""},
		{
			name: testInvalidValue, execute: func(genInvocation) error { return runtimeargv.ErrInvalid },
			want: `{"code":"generation_failed","message":"Compose generation failed",` +
				`"retryable":false}` + "\n",
		},
		{
			name: "retryable", execute: func(genInvocation) error { return registry.ErrRateLimited },
			want: `{"code":"generation_failed","message":"Compose generation failed",` +
				`"retryable":true}` + "\n",
		},
		{name: "cancelled", execute: func(genInvocation) error { return registry.ErrCancelled }, want: cancelledJSON},
		{name: "missing", execute: nil, want: internalErrorJSON},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			output := new(bytes.Buffer)
			status := run(context.Background(), []string{
				"gen", "--", testDockerRuntime, runOperation, testImageSource,
			}, output, test.execute, nil)
			if status != 0 && test.want == "" || status == 0 && test.want != "" || output.String() != test.want {
				t.Fatalf("run(gen) = %d, %q", status, output.String())
			}
		})
	}

	for _, err := range []error{context.Canceled, registry.ErrCancelled} {
		if classifyGenFailure(err).Code() != domain.ErrorOperationCancelled {
			t.Fatalf("classifyGenFailure(%v) did not cancel", err)
		}
	}
	if failure := classifyGenFailure(registry.ErrUnavailable); !failure.Retryable() {
		t.Fatal("classifyGenFailure(unavailable) is not retryable")
	}
}

func TestClassifyGenFailureReportsRuntimeNotBuilt(t *testing.T) {
	t.Parallel()

	if failure := classifyGenFailure(runtimeplugin.ErrNotBuilt); failure.Code() != domain.ErrorRuntimeNotBuilt {
		t.Fatalf("classifyGenFailure(runtime not built) = %q", failure.Code())
	}
}

func generatedImage(t *testing.T, platform domain.Platform) domain.ImageIdentity {
	t.Helper()

	digest, err := domain.ParseDigest("sha256:" + strings.Repeat("a", 64))
	if err != nil {
		t.Fatalf("ParseDigest() error = %v", err)
	}

	return domain.ImageIdentity{
		Origin:           domain.ImageOriginRegistry,
		Reference:        "",
		ReferenceDigest:  digest,
		Platform:         platform,
		PlatformManifest: digest,
		ImageConfig:      digest,
	}
}

func defaultGeneratedPlatform() domain.Platform {
	projection, err := runtimeargv.Parse(
		[]string{testDockerRuntime, runOperation, testImageSource}, "", testWorkingDirectory,
	)
	if err != nil {
		panic(err)
	}

	return projection.Platform()
}
