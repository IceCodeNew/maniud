package domain

import "testing"

func TestWorkloadOwnershipMatches(t *testing.T) {
	t.Parallel()

	desired := Hash([]byte("desired"))
	reference := Hash([]byte("reference"))
	imageConfig := Hash([]byte("image config"))
	ownership := WorkloadOwnership{
		Status:           OwnershipManaged,
		Service:          "api",
		Transaction:      "tx-1",
		DesiredState:     desired,
		Reference:        reference,
		ImageConfig:      imageConfig,
		PlatformManifest: Hash([]byte("platform manifest")),
	}

	if !ownership.Matches("api", "tx-1", desired, reference) {
		t.Fatal("WorkloadOwnership.Matches(exact) = false")
	}

	conflicting := ownership

	conflicting.Status = OwnershipConflicting
	if conflicting.Matches("api", "tx-1", desired, reference) ||
		ownership.Matches("worker", "tx-1", desired, reference) ||
		ownership.Matches("api", "tx-2", desired, reference) ||
		ownership.Matches("api", "tx-1", Hash(nil), reference) ||
		ownership.Matches("api", "tx-1", desired, Hash(nil)) {
		t.Fatal("WorkloadOwnership.Matches(conflict) = true")
	}
}
