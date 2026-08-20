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
	conflicts := []struct {
		name        string
		ownership   WorkloadOwnership
		service     string
		transaction string
		desired     Digest
		reference   Digest
	}{
		{name: "status", ownership: conflicting, service: "api", transaction: "tx-1", desired: desired, reference: reference},
		{name: "service", ownership: ownership, service: "worker", transaction: "tx-1", desired: desired, reference: reference},
		{name: "transaction", ownership: ownership, service: "api", transaction: "tx-2", desired: desired, reference: reference},
		{name: "desired state", ownership: ownership, service: "api", transaction: "tx-1", desired: Hash(nil), reference: reference},
		{name: "reference", ownership: ownership, service: "api", transaction: "tx-1", desired: desired, reference: Hash(nil)},
	}
	for _, conflict := range conflicts {
		if conflict.ownership.Matches(conflict.service, conflict.transaction, conflict.desired, conflict.reference) {
			t.Errorf("WorkloadOwnership.Matches(%s conflict) = true", conflict.name)
		}
	}
}

func TestIsOwnershipLabel(t *testing.T) {
	t.Parallel()

	for _, label := range []string{
		LabelService, LabelTransaction, LabelDesiredStateDigest, LabelReferenceDigest,
		LabelImageConfigDigest, LabelPlatformManifestDigest,
	} {
		if !IsOwnershipLabel(label) {
			t.Errorf("IsOwnershipLabel(%q) = false", label)
		}
	}
	if IsOwnershipLabel("io.maniud.unknown") {
		t.Fatal("IsOwnershipLabel(unknown) = true")
	}
}
