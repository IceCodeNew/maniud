package compose

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	composeTestDataPath         = "/data"
	composeTestWorkingDirectory = "/work"
)

var errMaterializeTest = errors.New("materialize test failure")

//nolint:cyclop,funlen // Each branch verifies one independent exact-snapshot invariant.
func TestMaterializeRuntimeExactSnapshotAndFailClosed(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	content := []byte("services: {}\n")
	files := map[string]RepositoryFile{
		remainingComposeEntry: {Content: content},
		"data/a.txt":          {Content: []byte("a\n")},
		"data/bin/run":        {Content: []byte("#!/bin/sh\n"), Executable: true},
	}
	snapshot := &RepositorySnapshot{Root: t.TempDir(), Entry: remainingComposeEntry, Files: files,
		RuntimePaths: []RepositoryPath{{Path: "data", Directory: true}}}
	snapshot.Digest = repositoryDigest(snapshot.Entry, snapshot.Files, snapshot.RuntimePaths)
	source := Source{Content: content, WorkingDir: snapshot.Root, Repository: snapshot}
	pinned, err := PinRepositoryRuntime(source, base)
	if err != nil || pinned.runtimeBase != base {
		t.Fatalf("PinRepositoryRuntime() = %#v, %v", pinned, err)
	}
	if err := pinned.MaterializeRuntime(); err != nil {
		t.Fatalf("MaterializeRuntime() error = %v", err)
	}
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := baseRoot.Close(); closeErr != nil {
			t.Errorf("close base root: %v", closeErr)
		}
	})
	if err := pinned.materializeRuntime(baseRoot, func(string) (os.FileInfo, error) {
		return nil, errMaterializeTest
	}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("materialize lstat failure = %v", err)
	}
	root := repositoryRuntimeRoot(base, snapshot.Digest)
	for name, want := range files {
		if name == snapshot.Entry {
			continue
		}
		runtimeRoot, openErr := os.OpenRoot(root)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(func() {
			if closeErr := runtimeRoot.Close(); closeErr != nil {
				t.Errorf("close runtime root: %v", closeErr)
			}
		})
		got, readErr := runtimeRoot.ReadFile(filepath.FromSlash(name))
		info, statErr := runtimeRoot.Stat(filepath.FromSlash(name))
		mode := runtimeFileMode
		if want.Executable {
			mode = runtimeExecutableMode
		}
		if readErr != nil || statErr != nil || !reflect.DeepEqual(got, want.Content) || info.Mode().Perm() != mode {
			t.Fatalf("materialized %s = %q mode %o, errors %v/%v", name, got, info.Mode().Perm(), readErr, statErr)
		}
	}
	if err := pinned.MaterializeRuntime(); err != nil {
		t.Fatalf("idempotent materialize: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra"), []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := pinned.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("extra entry error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "extra")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "data", "a.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pinned.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("mode drift error = %v", err)
	}
}

//nolint:cyclop // Each branch verifies one independent fail-closed boundary.
func TestMaterializeRuntimeRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	var source Source
	if err := source.MaterializeRuntime(); err != nil {
		t.Fatal(err)
	}
	if _, err := PinRepositoryRuntime(source, t.TempDir()); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	source.Repository = &RepositorySnapshot{RuntimePaths: []RepositoryPath{{Path: "data", Directory: true}}}
	if err := source.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if _, err := PinRepositoryRuntime(source, "relative"); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})
	if err := root.Mkdir(repositoryRuntimeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimeParent(root); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if err := syncRootDirectory(root, "missing"); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if validMaterializedRuntime(root, "missing", &RepositorySnapshot{}) {
		t.Fatal("missing runtime accepted")
	}
}

//nolint:cyclop,funlen,gocognit,gocyclo // Independent adapter validation branches form one focused matrix.
func TestWorkloadAdapterHelperSuccessAndFailureBoundaries(t *testing.T) {
	t.Parallel()

	var spec domain.WorkloadSpec
	weight := uint16(10)
	if !addBlkio(&spec, &composetypes.BlkioConfig{Weight: weight}) || spec.BlkioWeight == nil {
		t.Fatal("blkio")
	}
	if addBlkio(&spec, &composetypes.BlkioConfig{Weight: 9}) {
		t.Fatal("bad blkio")
	}
	d := composetypes.Duration(2 * time.Second)
	if !addStopTimeout(&spec, &d) || spec.StopTimeout == nil {
		t.Fatal("timeout")
	}
	badDuration := composetypes.Duration(time.Millisecond)
	if addStopTimeout(&spec, &badDuration) {
		t.Fatal("bad timeout")
	}
	if !addDevices(&spec, []composetypes.DeviceMapping{{Source: "/dev/a", Target: "/dev/b", Permissions: "rwm"}}) {
		t.Fatal("device")
	}
	for _, p := range []string{"", "rr", "x"} {
		if validDevicePermissions(p) {
			t.Fatalf("permission %q", p)
		}
	}
	if !addTmpfs(&spec, composetypes.StringList{"/run", "/tmp:ro,size=1m"}) ||
		addTmpfs(&spec, composetypes.StringList{"relative"}) {
		t.Fatal("tmpfs")
	}
	if !addUlimits(&spec, map[string]*composetypes.UlimitsConfig{"nofile": {Soft: 1, Hard: 2}}) ||
		addUlimits(&spec, map[string]*composetypes.UlimitsConfig{"": {}}) {
		t.Fatal("ulimit")
	}
	validPorts := []composetypes.ServicePortConfig{{
		Published: "80", Target: 81, Protocol: composeProtocolTCP, HostIP: "::1",
	}}
	invalidPorts := []composetypes.ServicePortConfig{{Published: "0", Target: 1, Protocol: composeProtocolTCP}}
	if !addPorts(&spec, validPorts) || addPorts(&spec, invalidPorts) {
		t.Fatal("ports")
	}
	if !validHostIP("") || validHostIP("01.2.3.4") {
		t.Fatal("host ip")
	}
	if !addSecurityOptions(&spec, []string{"no-new-privileges=true"}) || addSecurityOptions(&spec, []string{"label=x"}) {
		t.Fatal("security")
	}
	volumes := []composetypes.ServiceVolumeConfig{
		{Type: "volume", Target: "/cache"},
		{Type: "bind", Source: "/repo/data", Target: composeTestDataPath, ReadOnly: true},
	}
	if !addMounts(&spec, volumes, "/repo", "/runtime") || spec.Mounts[1].Source != "/runtime/data" {
		t.Fatalf("mounts %#v", spec.Mounts)
	}
	if addMounts(&spec, []composetypes.ServiceVolumeConfig{{Type: "tmpfs", Target: "/x"}}, "", "") {
		t.Fatal("mount type")
	}
	if !emptyVolumeOptions(nil) || emptyVolumeOptions(&composetypes.ServiceVolumeVolume{NoCopy: true}) {
		t.Fatal("volume options")
	}
	zero := uint64(0)
	health := &composetypes.HealthCheckConfig{Test: []string{contractHealth, contractTrue}, Retries: &zero}
	if !addHealthcheck(&spec, health) || spec.Healthcheck == nil || spec.Healthcheck.Retries != nil {
		t.Fatal("health")
	}
	if addHealthcheck(&spec, &composetypes.HealthCheckConfig{Disable: true, Test: []string{"NONE"}}) {
		t.Fatal("disabled health with test")
	}
	if durationString(nil) != "" || durationString(&d) != "2s" {
		t.Fatal("duration")
	}
	if cloneMapping(nil) != nil || clonePointer[int](nil) != nil || truePointer(false) != nil || hostsList(nil) != nil {
		t.Fatal("nil helpers")
	}
}

//nolint:cyclop // Each assertion exercises one independent serialization shape.
func TestRuntimeWriterHelperCompleteShapes(t *testing.T) {
	t.Parallel()

	if got, _ := (runtimeMount{short: composeTestDataPath}).MarshalYAML(); got != composeTestDataPath {
		t.Fatal(got)
	}
	gotMount, _ := (runtimeMount{bind: &runtimeBindMount{Type: "bind"}}).MarshalYAML()
	if reflect.TypeOf(gotMount) != reflect.TypeFor[runtimeBindMount]() {
		t.Fatal(gotMount)
	}
	if got, _ := (runtimeUlimit{Soft: 2, Hard: 2}).MarshalYAML(); got != int64(2) {
		t.Fatal(got)
	}
	if got, _ := (runtimeUlimit{Soft: 1, Hard: 2}).MarshalYAML(); reflect.TypeOf(got).Kind() != reflect.Struct {
		t.Fatal(got)
	}
	platform := domain.Platform{OS: archiveLinuxOS, Architecture: "arm64", Variant: "v8"}
	if formatPlatform(platform) != "linux/arm64/v8" {
		t.Fatal("platform")
	}
	if got, err := runtimeEnvironmentFiles(nil, composeTestWorkingDirectory); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if _, err := runtimeEnvironmentFiles(
		[]string{composeTestWorkingDirectory},
		composeTestWorkingDirectory,
	); err == nil {
		t.Fatal("directory env accepted")
	}
	got, err := runtimeEnvironmentFiles([]string{"/work/a.env"}, composeTestWorkingDirectory)
	if err != nil || !reflect.DeepEqual(got, []string{"a.env"}) {
		t.Fatal(got, err)
	}
	ports := runtimePorts([]domain.PortBinding{
		{PublishedPort: 79, TargetPort: 80, Protocol: composeProtocolTCP},
		{HostIP: "::1", PublishedPort: 80, TargetPort: 81, Protocol: "udp"},
	})
	if !reflect.DeepEqual(ports, []string{"79:80", "[::1]:80:81/udp"}) {
		t.Fatal(ports)
	}
	if got := runtimeTmpfs([]domain.TmpfsMount{{Target: "/run"}, {Target: "/tmp", Options: []string{"ro"}}}); !reflect.DeepEqual(got, []string{"/run", "/tmp:ro"}) {
		t.Fatal(got)
	}
	if runtimeUlimits(nil) != nil || len(runtimeMounts([]domain.Mount{{Kind: domain.MountVolume, Target: "/v"}})) != 1 {
		t.Fatal("runtime helpers")
	}
}

//nolint:cyclop // The table verifies independent malformed repository-document shapes.
func TestRepositoryCollectorMalformedShapes(t *testing.T) {
	t.Parallel()

	var docs []repositoryDocument
	invalidIncludes := []any{
		[]any{},
		[]any{1},
		[]any{map[string]any{repositoryPathKey: "a", "extra": true}},
		[]any{map[string]any{repositoryPathKey: nil}},
	}
	for _, raw := range invalidIncludes {
		if collectIncludes(raw, ".", &docs) {
			t.Fatalf("include accepted %#v", raw)
		}
	}
	invalidPaths := []any{map[string]any{"extra": 1}}
	if collectResourceFiles("bad", ".", &docs) ||
		collectExtends("bad", ".", &docs) ||
		collectPathList(invalidPaths, ".", &docs) {
		t.Fatal("collector accepted malformed")
	}
	if _, ok := repositoryPaths(nil, "."); ok {
		t.Fatal("nil paths")
	}
	if collectBindMounts("bad", ".", new([]string)) || collectBindMounts([]any{1}, ".", new([]string)) {
		t.Fatal("bind shape")
	}
	for _, raw := range []any{"", "/abs", "a:b", "$A", "~user/a", 1} {
		if _, ok := resolveRepositoryPath(raw, "."); ok {
			t.Fatalf("path accepted %#v", raw)
		}
	}
	if _, ok := repositoryEnvironment(nil, RepositoryFile{Content: []byte("bad='")}); ok {
		t.Fatal("bad dotenv")
	}
	if _, ok := repositoryDefaultEnvironment(".", map[string]string{composeDisableEnvFile: "maybe"}); ok {
		t.Fatal("bad bool")
	}
	if value, ok := repositoryDefaultEnvironment(".", map[string]string{composeDisableEnvFile: "false"}); !ok || value == "" {
		t.Fatalf("false disable flag = %q, %t", value, ok)
	}
	if strings.TrimSpace(resolveRepositoryDefaultEnv(".")) == "" {
		t.Fatal("default env")
	}
}
