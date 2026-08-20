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
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

type genResolverFixture struct {
	image    domain.ImageIdentity
	err      error
	source   imageref.Source
	platform domain.Platform
}

var errGeneratedComposeTest = errors.New("generated Compose test failure")

type generatedComposeFailureCase struct {
	name      string
	configure func(*generatedComposeOperations)
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
		name:   testServiceName,
		output: "generated/service.yaml",
	}, output, genDependencies{
		workingDirectory: testWorkingDirectory,
		images:           resolver,
		write: func(path string, content []byte) error {
			writtenPath = path
			writtenContent = append([]byte(nil), content...)

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
		write: func(string, []byte) error { return nil },
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
	registryProjection, err := runtimeargv.ParseSource(testRegistrySource, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolveGeneratedImage(context.Background(), registryProjection, genDependencies{})
	if !errors.Is(err, registry.ErrUnavailable) {
		t.Fatalf("resolveGeneratedImage(missing registry) = %v", err)
	}
}

func TestExecuteGenAnalyzesArchiveWithoutImporting(t *testing.T) {
	t.Parallel()

	archivePath := writeGenArchive(t)
	writtenPath := ""
	writtenContent := []byte(nil)
	output := new(bytes.Buffer)
	err := executeGen(context.Background(), genInvocation{
		source: "docker-archive:" + archivePath + ":example.com/team/app:1",
	}, output, genDependencies{
		workingDirectory: testWorkingDirectory,
		analyzeArchive:   imagearchive.Analyze,
		write: func(path string, content []byte) error {
			writtenPath = path
			writtenContent = bytes.Clone(content)

			return nil
		},
	})
	if err != nil {
		t.Fatalf("executeGen() error = %v", err)
	}
	if writtenPath != testWorkingDirectory+"/app.yaml" ||
		!bytes.Contains(writtenContent, []byte("kind: docker-archive")) ||
		bytes.Contains(writtenContent, []byte(archivePath)) {
		t.Fatalf("executeGen() = path %q, content %q", writtenPath, writtenContent)
	}
	if !strings.Contains(output.String(), `"import_command":"docker image load --input`) ||
		!strings.Contains(output.String(), `"status":"generated"`) {
		t.Fatalf("executeGen() output = %q", output.String())
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
				write:            func(string, []byte) error { return nil },
			})
			if !errors.Is(err, test.want) {
				t.Fatalf("executeGen() error = %v, want %v", err, test.want)
			}
		})
	}
}

func writeGenArchive(t *testing.T) string {
	t.Helper()

	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{},` +
		`"config":{"Entrypoint":["/init"],"Cmd":["serve"]}}`)
	layer := []byte("layer")
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
				write = func(path string, _ []byte) error {
					writePath = path

					return nil
				}
			}

			err := executeGen(context.Background(), test.arguments, test.output, genDependencies{
				workingDirectory: testWorkingDirectory,
				images:           test.resolver,
				write:            write,
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
	write         func(string, []byte) error
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
			arguments:     genInvocation{source: testImageSource},
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
			write: func(string, []byte) error { return io.ErrClosedPipe }, want: io.ErrClosedPipe,
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

	dependencies, err := defaultGenDependencies(map[string]string{}, func() (string, error) {
		return testWorkingDirectory, nil
	})
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

	for _, getWorkingDirectory := range []func() (string, error){
		func() (string, error) { return "", io.ErrClosedPipe },
		func() (string, error) { return "relative", nil },
	} {
		if _, err := defaultGenDependencies(nil, getWorkingDirectory); err == nil {
			t.Fatal("defaultGenDependencies(invalid cwd) succeeded")
		}
	}
}

func assertDefaultGenDependencies(t *testing.T, dependencies genDependencies, err error) {
	t.Helper()

	if err != nil || dependencies.workingDirectory != testWorkingDirectory ||
		dependencies.images == nil || dependencies.runtimeImage == nil ||
		dependencies.analyzeArchive == nil || dependencies.write == nil {
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
		genInvocation{source: testImageSource, runtimeArgs: []string{testDockerRuntime}},
		testWorkingDirectory,
	)
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("parseGenProjection(ambiguous) error = %v", err)
	}
	_, err = parseGenProjection(genInvocation{source: "\x00"}, testWorkingDirectory)
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("parseGenProjection(invalid source) error = %v", err)
	}

	archivePath := writeGenArchive(t)
	rendered, name, command, err := renderArchiveGen(context.Background(), genInvocation{
		source: "docker-archive:" + archivePath + "@0", name: "bad name",
	}, genDependencies{analyzeArchive: imagearchive.Analyze})
	if err == nil {
		t.Fatalf("renderArchiveGen(invalid name) = %q, %q, %q", rendered, name, command)
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
		source: testImageSource,
		output: invalidPathValue,
	}, genDependencies{workingDirectory: testWorkingDirectory})
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("renderGen(registry invalid output) error = %v", err)
	}

	_, _, err = generatedComposePath(invalidPathValue, testServiceName, testWorkingDirectory)
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("generatedComposePath(invalid output) error = %v", err)
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
