package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	unknownTestValue  = "unknown"
	invalidTestDigest = "sha256:invalid"
)

func validManifestForTest(t *testing.T) Manifest {
	t.Helper()

	archive := makeTar(t, regular("data", "payload"))
	inventory, err := Analyze(context.Background(), bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("Analyze fixture: %v", err)
	}

	return Manifest{
		Version:               manifestVersion,
		OperationToken:        Identifier{1},
		TransactionID:         Identifier{2},
		BaseTransactionID:     Identifier{3},
		Project:               "project",
		Service:               "service",
		Runtime:               domain.RuntimeDocker,
		CreatedUnix:           1_700_000_000,
		SourceDigest:          domain.Hash([]byte("source")),
		EffectiveDigest:       domain.Hash([]byte("effective")),
		ExecutionDigest:       domain.Hash([]byte("execution")),
		PredecessorWorkloadID: "predecessor",
		Artifacts: []Artifact{
			{
				Mount: domain.RuntimeMount{
					Kind: domain.MountBind, Source: "/private/data", Target: "/data",
				},
				ProvenanceDigest: domain.Hash([]byte("git provenance")),
				FileName:         artifactName(0),
				Inventory:        inventory,
			},
			{
				Mount: domain.RuntimeMount{
					Kind: domain.MountVolume, Name: "volume", Source: "/runtime/volume", Target: "/state",
				},
				FileName:  artifactName(1),
				Inventory: inventory,
			},
		},
	}
}

func cloneManifest(value Manifest) Manifest {
	value.Artifacts = append([]Artifact(nil), value.Artifacts...)

	return value
}

func TestManifestCanonicalRoundTrip(t *testing.T) {
	t.Parallel()

	want := validManifestForTest(t)
	raw, digest, err := EncodeManifest(want)
	if err != nil {
		t.Fatalf("EncodeManifest: %v", err)
	}
	if digest != domain.Hash(raw) || len(raw) > maximumManifestBytes {
		t.Fatalf("manifest digest/size = %s/%d", digest, len(raw))
	}
	assertManifestRoundTrip(t, raw, digest)
	assertManifestPath(t, want.TransactionID)
}

func assertManifestRoundTrip(t *testing.T, raw []byte, digest domain.Digest) {
	t.Helper()

	got, gotDigest, err := DecodeManifest(bytes.NewReader(raw))
	if err != nil || gotDigest != digest {
		t.Fatalf("DecodeManifest = %#v, %s, %v", got, gotDigest, err)
	}
	reencoded, _, err := EncodeManifest(got)
	if err != nil || !bytes.Equal(raw, reencoded) {
		t.Fatalf("canonical re-encode differs: %v\n%s\n%s", err, raw, reencoded)
	}
}

func assertManifestPath(t *testing.T, transaction Identifier) {
	t.Helper()

	path, err := ManifestPath(transaction)
	if err != nil || path != transaction.String()+"/"+manifestName {
		t.Fatalf("ManifestPath = %q, %v", path, err)
	}
	if _, err := ManifestPath(Identifier{}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("ManifestPath(zero) = %v", err)
	}
}

func TestDecodeManifestRejectsNoncanonicalJSON(t *testing.T) {
	t.Parallel()

	raw, _, err := EncodeManifest(validManifestForTest(t))
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string][]byte{
		"malformed":      []byte("{"),
		"duplicate":      bytes.Replace(raw, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		unknownTestValue: bytes.Replace(raw, []byte(`{"version":1`), []byte(`{"unknown":1,"version":1`), 1),
		"leading space":  append([]byte(" "), raw...),
		"trailing value": append(append([]byte(nil), raw...),
			[]byte(" true")...),
		"oversized": bytes.Repeat([]byte(" "), maximumManifestBytes+1),
	}
	for name, value := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, _, decodeErr := DecodeManifest(bytes.NewReader(value))
			if !errors.Is(decodeErr, ErrInvalidManifest) {
				t.Fatalf("DecodeManifest error = %v", decodeErr)
			}
		})
	}
}

func TestManifestRejectsInvalidIdentityFields(t *testing.T) {
	t.Parallel()

	valid := validManifestForTest(t)
	mutations := []func(*Manifest){
		func(value *Manifest) { value.Version = 0 },
		func(value *Manifest) { value.OperationToken = Identifier{} },
		func(value *Manifest) { value.TransactionID = Identifier{} },
		func(value *Manifest) { value.BaseTransactionID = Identifier{} },
		func(value *Manifest) { value.Project = "" },
		func(value *Manifest) { value.Project = strings.Repeat("x", maximumIdentityBytes+1) },
		func(value *Manifest) { value.Service = "bad\x00service" },
		func(value *Manifest) { value.Runtime = domain.RuntimeKind("invalid") },
		func(value *Manifest) { value.CreatedUnix = 0 },
		func(value *Manifest) { value.SourceDigest = domain.Digest{} },
		func(value *Manifest) { value.EffectiveDigest = domain.Digest{} },
		func(value *Manifest) { value.ExecutionDigest = domain.Digest{} },
		func(value *Manifest) { value.PredecessorWorkloadID = "" },
		func(value *Manifest) { value.Artifacts = nil },
		func(value *Manifest) { value.Artifacts[1].Mount.Target = value.Artifacts[0].Mount.Target },
		func(value *Manifest) { value.Artifacts[0], value.Artifacts[1] = value.Artifacts[1], value.Artifacts[0] },
		func(value *Manifest) { value.Artifacts[0].FileName = "different.tar" },
	}
	for index, mutate := range mutations {
		value := cloneManifest(valid)
		mutate(&value)
		if _, _, err := EncodeManifest(value); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("mutation %d error = %v", index, err)
		}
	}
}

func TestManifestRejectsInvalidArtifacts(t *testing.T) {
	t.Parallel()

	valid := validManifestForTest(t)
	mutations := []func(*Artifact){
		func(value *Artifact) { value.Mount.ReadOnly = true },
		func(value *Artifact) { value.Mount.Source = "relative" },
		func(value *Artifact) { value.Mount.Target = "/" },
		func(value *Artifact) { value.Mount.Target = "/bad/../target" },
		func(value *Artifact) { value.Inventory.ArchiveBytes-- },
		func(value *Artifact) { value.Inventory.PayloadBytes = value.Inventory.ArchiveBytes + 1 },
		func(value *Artifact) { value.Mount.Name = "bind-name" },
		func(value *Artifact) { value.ProvenanceDigest = domain.Digest{} },
		func(value *Artifact) { value.Mount.Kind = domain.MountKind(255) },
	}
	for index, mutate := range mutations {
		value := cloneManifest(valid)
		mutate(&value.Artifacts[0])
		if _, _, err := EncodeManifest(value); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("bind mutation %d error = %v", index, err)
		}
	}

	volumeMutations := []func(*Artifact){
		func(value *Artifact) { value.Mount.Name = "" },
		func(value *Artifact) { value.ProvenanceDigest = domain.Hash([]byte("unexpected")) },
	}
	for index, mutate := range volumeMutations {
		value := cloneManifest(valid)
		mutate(&value.Artifacts[1])
		if _, _, err := EncodeManifest(value); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("volume mutation %d error = %v", index, err)
		}
	}
}

func TestManifestRejectsEncodedFieldViolations(t *testing.T) {
	t.Parallel()

	valid := manifestToWire(validManifestForTest(t))
	tests := []func(*manifestWire){
		func(value *manifestWire) { value.OperationToken = "A" + value.OperationToken[1:] },
		func(value *manifestWire) { value.TransactionID = "zz" + value.TransactionID[2:] },
		func(value *manifestWire) { value.BaseTransactionID = strings.Repeat("0", 32) },
		func(value *manifestWire) { value.Runtime = unknownTestValue },
		func(value *manifestWire) { value.SourceDigest = invalidTestDigest },
		func(value *manifestWire) { value.EffectiveDigest = invalidTestDigest },
		func(value *manifestWire) { value.ExecutionDigest = invalidTestDigest },
		func(value *manifestWire) { value.Artifacts[0].Kind = unknownTestValue },
		func(value *manifestWire) { value.Artifacts[0].ArchiveDigest = invalidTestDigest },
		func(value *manifestWire) { value.Artifacts[0].SemanticDigest = invalidTestDigest },
		func(value *manifestWire) { value.Artifacts[0].ProvenanceDigest = invalidTestDigest },
	}
	for index, mutate := range tests {
		wire := valid
		wire.Artifacts = append([]artifactWire(nil), valid.Artifacts...)
		mutate(&wire)
		raw, err := marshalWire(wire)
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = DecodeManifest(bytes.NewReader(raw))
		if !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("wire mutation %d error = %v", index, err)
		}
	}
}

func TestManifestBoundsCanonicalSizeAndArtifactCount(t *testing.T) {
	t.Parallel()

	valid := validManifestForTest(t)
	large := cloneManifest(valid)
	large.Artifacts = make([]Artifact, 500)
	for index := range large.Artifacts {
		artifact := valid.Artifacts[1]
		artifact.Mount.Target = fmt.Sprintf("/target/%04d", index)
		artifact.FileName = artifactName(index)
		large.Artifacts[index] = artifact
	}
	if _, _, err := EncodeManifest(large); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("oversized canonical manifest error = %v", err)
	}

	wire := manifestToWire(valid)
	wire.Artifacts = make([]artifactWire, maximumManifestMounts+1)
	if _, ok := manifestFromWire(wire); ok {
		t.Fatal("manifestFromWire accepted too many artifacts")
	}
}

func TestManifestHelpers(t *testing.T) {
	t.Parallel()

	identifier := Identifier{0xab}
	if parsed, valid := parseIdentifier(identifier.String()); !valid || parsed != identifier {
		t.Fatalf("parseIdentifier = %x, %t", parsed, valid)
	}
	invalidIdentifiers := []string{
		"", strings.ToUpper(identifier.String()), strings.Repeat("z", 32), strings.Repeat("0", 32),
	}
	for _, invalid := range invalidIdentifiers {
		if _, valid := parseIdentifier(invalid); valid {
			t.Errorf("parseIdentifier(%q) succeeded", invalid)
		}
	}

	if digest, valid := parseOptionalDigest(""); !valid || digest != (domain.Digest{}) {
		t.Fatalf("empty optional digest = %s, %t", digest, valid)
	}
	if _, valid := parseOptionalDigest("bad"); valid {
		t.Fatal("invalid optional digest accepted")
	}

	if mountKindString(domain.MountKind(255)) != "" {
		t.Fatal("unknown mount kind has a wire name")
	}
	if _, valid := parseMountKind(unknownTestValue); valid {
		t.Fatal("unknown wire mount kind accepted")
	}
}

func marshalWire(wire manifestWire) ([]byte, error) {
	raw, err := json.Marshal(wire)
	if err != nil {
		return nil, fmt.Errorf("marshal manifest fixture: %w", err)
	}

	return raw, nil
}
