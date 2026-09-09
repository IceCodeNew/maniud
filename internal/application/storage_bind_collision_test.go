package application

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestUpgradeKeepsDistinctBindTargetsAcrossRetry(t *testing.T) {
	t.Parallel()
	const firstTarget = "/first/data"
	const secondTarget = "/second/data"
	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.archives = map[string][]byte{
		firstTarget:  upgradeTestArchive(t, "first-payload"),
		secondTarget: upgradeTestArchive(t, "second-payload"),
	}
	mounts := []domain.Mount{
		{Kind: domain.MountBind, Source: "/new/first", Target: firstTarget},
		{Kind: domain.MountBind, Source: "/new/second", Target: secondTarget},
	}
	mutation.preparation.Workload.Mounts = slices.Clone(mounts)
	mutation.preparation.Plan.Observation.RuntimeMounts = []domain.RuntimeMount{
		{Kind: domain.MountBind, Source: "/old/first", Target: firstTarget},
		{Kind: domain.MountBind, Source: "/old/second", Target: secondTarget},
	}
	runtime.getErrAt = map[int]error{5: errTestBoundary}
	if err := runUpgrade(t.Context(), mutation, runtime, bootstrapCredentials{}); !errors.Is(err, errTestBoundary) {
		t.Fatalf("interrupted upgrade = %v", err)
	}
	parent := filepath.Join(mutation.backupRoot, "replacements", mutation.preparation.Transaction.ID.String())
	want := []domain.Mount{
		{Kind: domain.MountBind, Target: firstTarget,
			Source: filepath.Join(parent, "cc7df2440c067a5a44f6457960f2ae5fc6dbaab39eb569184cd5ffc3fff8a2e3")},
		{Kind: domain.MountBind, Target: secondTarget,
			Source: filepath.Join(parent, "c59b8acc2db4b3a74e0ee172eb8062252f7b7440c82b061659c9c1c74562a775")},
	}
	if !slices.Equal(runtime.lastCreated.Mounts, want) {
		t.Fatalf("created mounts = %+v, want %+v", runtime.lastCreated.Mounts, want)
	}
	mutation.preparation.Plan.Kind = PlanProbeUnknownEffect
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	mutation.preparation.Workload.Mounts = slices.Clone(mounts)
	delete(runtime.getErrAt, 5)
	if err := runUpgrade(t.Context(), mutation, runtime, bootstrapCredentials{}); err != nil ||
		mutation.preparation.Transaction.State != store.TransactionSucceeded || runtime.creates != 1 ||
		!slices.Equal(mutation.preparation.Workload.Mounts, want) {
		t.Fatalf("retry = %v; state %s, creates %d, mounts %+v", err,
			mutation.preparation.Transaction.State, runtime.creates, mutation.preparation.Workload.Mounts)
	}
}

func TestReplacementBindsRejectLegacyTransactionDirectory(t *testing.T) {
	t.Parallel()
	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.archives = map[string][]byte{testVolumeTarget: upgradeTestArchive(t, "payload")}
	mount := domain.Mount{Kind: domain.MountBind, Source: testBindSourceNew, Target: testVolumeTarget}
	mutation.preparation.Workload.Mounts = []domain.Mount{mount}
	sources := []backedStorageSource{{Mount: domain.RuntimeMount{
		Kind: domain.MountBind, Source: testBindSourceOld, Target: testVolumeTarget,
	}}}
	legacy := filepath.Join(mutation.backupRoot, "replacements", mutation.preparation.Transaction.ID.String(), "data")
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	err := prepareUpgradeReplacementBinds(&upgradeExecution{mutation: mutation, sources: sources})
	if !errors.Is(err, ErrConflictingState) || mutation.preparation.Workload.Mounts[0] != mount {
		t.Fatalf("legacy replay changed directory: %v, %+v", err, mutation.preparation.Workload.Mounts)
	}
	entries, err := os.ReadDir(filepath.Dir(legacy))
	if err != nil || len(entries) != 1 || entries[0].Name() != "data" {
		t.Fatalf("legacy directory was changed: %+v, %v", entries, err)
	}
}

func TestReplacementBindsPreserveNonemptyCurrentDirectory(t *testing.T) {
	t.Parallel()
	fixture := newStorageTestFixture(t, false)
	mount := domain.Mount{Kind: domain.MountBind, Source: testBindSourceNew, Target: testVolumeTarget}
	fixture.mutation.preparation.Workload.Mounts = []domain.Mount{mount}
	path, err := replacementBindPath(fixture.mutation.backupRoot,
		fixture.mutation.preparation.Transaction.ID.String(), mount)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(path, "existing-data")
	if err = os.WriteFile(file, []byte("preserve this"), 0o600); err != nil {
		t.Fatal(err)
	}
	execution := &upgradeExecution{mutation: fixture.mutation, sources: []backedStorageSource{{
		Mount: domain.RuntimeMount{Kind: domain.MountBind, Source: testBindSourceOld, Target: testVolumeTarget},
	}}}
	err = prepareUpgradeReplacementBinds(execution)
	if !errors.Is(err, ErrConflictingState) || fixture.mutation.preparation.Workload.Mounts[0] != mount {
		t.Fatalf("occupied replacement changed mount: %v, %+v", err, fixture.mutation.preparation.Workload.Mounts)
	}
	//nolint:gosec // This path is the test-owned replacement file created above.
	content, err := os.ReadFile(file)
	if err != nil || string(content) != "preserve this" {
		t.Fatalf("existing data changed: %q, %v", content, err)
	}
}
