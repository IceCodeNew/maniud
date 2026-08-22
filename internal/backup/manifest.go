package backup

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	manifestVersion       = 1
	maximumManifestBytes  = 64 * 1024
	maximumManifestMounts = 4096
	maximumIdentityBytes  = 256
	manifestName          = "manifest.json"
	identifierBytes       = 16
	bindMountName         = "bind"
	volumeMountName       = "volume"
)

var (
	// ErrInvalidManifest reports a backup manifest outside the canonical v1 contract.
	ErrInvalidManifest = errors.New("persistent workload backup manifest is invalid")
	// ErrInvalidBackupRoot reports an unsafe or replaced private backup directory.
	ErrInvalidBackupRoot = errors.New("persistent workload backup root is invalid")
	// ErrInsufficientCapacity reports that a publication would consume the
	// destination's required byte or inode safety margin.
	ErrInsufficientCapacity = errors.New("persistent workload backup capacity is insufficient")
	// ErrPublicationConflict reports an existing different backup for a transaction.
	ErrPublicationConflict = errors.New("persistent workload backup publication conflicts")
	// ErrPublicationUnknown reports that durable publication may have taken effect.
	ErrPublicationUnknown = errors.New("persistent workload backup publication outcome is unknown")
)

// Identifier is one opaque 128-bit backup operation or transaction identity.
type Identifier [identifierBytes]byte

// String returns the lowercase hexadecimal identity.
func (identifier Identifier) String() string {
	return hex.EncodeToString(identifier[:])
}

// Manifest binds complete archive artifacts to one upgrade transaction.
type Manifest struct {
	Version               int
	OperationToken        Identifier
	TransactionID         Identifier
	BaseTransactionID     Identifier
	Project               string
	Service               string
	Runtime               domain.RuntimeKind
	CreatedUnix           int64
	SourceDigest          domain.Digest
	EffectiveDigest       domain.Digest
	ExecutionDigest       domain.Digest
	PredecessorWorkloadID string
	Artifacts             []Artifact
}

// Artifact binds one writable runtime mount to a verified tar stream.
type Artifact struct {
	Mount            domain.RuntimeMount
	ProvenanceDigest domain.Digest
	FileName         string
	Inventory        Inventory
}

type manifestWire struct {
	Version               int            `json:"version"`
	OperationToken        string         `json:"operation_token"`
	TransactionID         string         `json:"transaction_id"`
	BaseTransactionID     string         `json:"base_transaction_id"`
	Project               string         `json:"project"`
	Service               string         `json:"service"`
	Runtime               string         `json:"runtime"`
	CreatedUnix           int64          `json:"created_unix"`
	SourceDigest          string         `json:"source_digest"`
	EffectiveDigest       string         `json:"effective_digest"`
	ExecutionDigest       string         `json:"execution_digest"`
	PredecessorWorkloadID string         `json:"predecessor_workload_id"`
	Artifacts             []artifactWire `json:"artifacts"`
}

type artifactWire struct {
	Kind             string `json:"kind"`
	Name             string `json:"name"`
	Source           string `json:"source"`
	Target           string `json:"target"`
	ProvenanceDigest string `json:"provenance_digest"`
	FileName         string `json:"file_name"`
	EntryCount       int64  `json:"entry_count"`
	PayloadBytes     int64  `json:"payload_bytes"`
	ArchiveBytes     int64  `json:"archive_bytes"`
	ArchiveDigest    string `json:"archive_digest"`
	SemanticDigest   string `json:"semantic_digest"`
}

// EncodeManifest returns the canonical bounded JSON and its SHA-256 identity.
func EncodeManifest(manifest Manifest) ([]byte, domain.Digest, error) {
	if !validManifest(manifest) {
		return nil, domain.Digest{}, ErrInvalidManifest
	}

	raw := canonicalManifest(manifest)
	if len(raw) > maximumManifestBytes {
		return nil, domain.Digest{}, ErrInvalidManifest
	}

	return raw, domain.Hash(raw), nil
}

// DecodeManifest accepts only the canonical JSON produced by EncodeManifest.
func DecodeManifest(reader io.Reader) (Manifest, domain.Digest, error) {
	raw, valid := jsonstrict.Read(reader, maximumManifestBytes)
	if !valid {
		return Manifest{}, domain.Digest{}, ErrInvalidManifest
	}

	var wire manifestWire
	if !jsonstrict.Decode(bytes.NewReader(raw), maximumManifestBytes, &wire) {
		return Manifest{}, domain.Digest{}, ErrInvalidManifest
	}

	manifest, valid := manifestFromWire(wire)
	if !valid {
		return Manifest{}, domain.Digest{}, ErrInvalidManifest
	}

	canonical := canonicalManifest(manifest)
	if !bytes.Equal(raw, canonical) {
		return Manifest{}, domain.Digest{}, ErrInvalidManifest
	}

	return manifest, domain.Hash(canonical), nil
}

// ManifestPath returns the fixed path relative to the private backup root.
func ManifestPath(transaction Identifier) (string, error) {
	if transaction == (Identifier{}) {
		return "", ErrInvalidManifest
	}

	return transaction.String() + "/" + manifestName, nil
}

func validManifest(manifest Manifest) bool {
	if !validManifestIdentity(manifest) || !validManifestDigests(manifest) ||
		len(manifest.Artifacts) == 0 || len(manifest.Artifacts) > maximumManifestMounts {
		return false
	}

	for index, artifact := range manifest.Artifacts {
		if !validArtifact(artifact, index) ||
			index > 0 && manifest.Artifacts[index-1].Mount.Target >= artifact.Mount.Target {
			return false
		}
	}

	return true
}

func validManifestIdentity(manifest Manifest) bool {
	return manifest.Version == manifestVersion && manifest.OperationToken != (Identifier{}) &&
		manifest.TransactionID != (Identifier{}) && manifest.BaseTransactionID != (Identifier{}) &&
		validIdentityText(manifest.Project) && validIdentityText(manifest.Service) &&
		manifest.Runtime.SupportsWorkloads() && manifest.CreatedUnix > 0
}

func validManifestDigests(manifest Manifest) bool {
	return manifest.SourceDigest != (domain.Digest{}) && manifest.EffectiveDigest != (domain.Digest{}) &&
		manifest.ExecutionDigest != (domain.Digest{}) &&
		validBoundedText(manifest.PredecessorWorkloadID, maximumPathBytes)
}

func validArtifact(artifact Artifact, index int) bool {
	mount := artifact.Mount
	if !validArtifactIdentity(artifact, index) {
		return false
	}

	switch mount.Kind {
	case domain.MountBind:
		return mount.Name == "" && artifact.ProvenanceDigest != (domain.Digest{})
	case domain.MountVolume:
		return validBoundedText(mount.Name, maximumIdentityBytes) &&
			artifact.ProvenanceDigest == (domain.Digest{})
	default:
		return false
	}
}

func validArtifactIdentity(artifact Artifact, index int) bool {
	mount := artifact.Mount

	return !mount.ReadOnly && validAbsolutePath(mount.Source) && validContainerPath(mount.Target) &&
		artifact.FileName == artifactName(index) && validInventory(artifact.Inventory)
}

func validIdentityText(value string) bool {
	return validBoundedText(value, maximumIdentityBytes)
}

func validBoundedText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) &&
		!strings.ContainsRune(value, 0)
}

func validAbsolutePath(value string) bool {
	return validBoundedText(value, maximumPathBytes) && strings.HasPrefix(value, "/") &&
		path.Clean(value) == value
}

func validContainerPath(value string) bool {
	return validAbsolutePath(value) && value != "/"
}

func artifactName(index int) string {
	return fmt.Sprintf("artifact-%06d.tar", index+1)
}

func canonicalManifest(manifest Manifest) []byte {
	raw, _ := json.Marshal(manifestToWire(manifest)) //nolint:errchkjson // The fixed wire type cannot fail encoding.

	return raw
}

func manifestToWire(manifest Manifest) manifestWire {
	artifacts := make([]artifactWire, len(manifest.Artifacts))
	for index, artifact := range manifest.Artifacts {
		artifacts[index] = artifactToWire(artifact)
	}

	return manifestWire{
		Version:               manifest.Version,
		OperationToken:        manifest.OperationToken.String(),
		TransactionID:         manifest.TransactionID.String(),
		BaseTransactionID:     manifest.BaseTransactionID.String(),
		Project:               manifest.Project,
		Service:               manifest.Service,
		Runtime:               manifest.Runtime.String(),
		CreatedUnix:           manifest.CreatedUnix,
		SourceDigest:          manifest.SourceDigest.String(),
		EffectiveDigest:       manifest.EffectiveDigest.String(),
		ExecutionDigest:       manifest.ExecutionDigest.String(),
		PredecessorWorkloadID: manifest.PredecessorWorkloadID,
		Artifacts:             artifacts,
	}
}

func artifactToWire(artifact Artifact) artifactWire {
	provenance := ""
	if artifact.ProvenanceDigest != (domain.Digest{}) {
		provenance = artifact.ProvenanceDigest.String()
	}

	return artifactWire{
		Kind:             mountKindString(artifact.Mount.Kind),
		Name:             artifact.Mount.Name,
		Source:           artifact.Mount.Source,
		Target:           artifact.Mount.Target,
		ProvenanceDigest: provenance,
		FileName:         artifact.FileName,
		EntryCount:       artifact.Inventory.EntryCount,
		PayloadBytes:     artifact.Inventory.PayloadBytes,
		ArchiveBytes:     artifact.Inventory.ArchiveBytes,
		ArchiveDigest:    artifact.Inventory.ArchiveDigest.String(),
		SemanticDigest:   artifact.Inventory.SemanticDigest.String(),
	}
}

func manifestFromWire(wire manifestWire) (Manifest, bool) {
	operation, transaction, base, identifiersValid := parseManifestIdentifiers(wire)
	runtime, runtimeValid := domain.ParseRuntimeKind(wire.Runtime)
	source, effective, execution, digestsValid := parseManifestDigests(wire)
	if !identifiersValid || !runtimeValid || !digestsValid || len(wire.Artifacts) > maximumManifestMounts {
		return Manifest{}, false
	}

	artifacts := make([]Artifact, len(wire.Artifacts))
	for index, encoded := range wire.Artifacts {
		artifact, valid := artifactFromWire(encoded)
		if !valid {
			return Manifest{}, false
		}
		artifacts[index] = artifact
	}

	manifest := Manifest{
		Version:               wire.Version,
		OperationToken:        operation,
		TransactionID:         transaction,
		BaseTransactionID:     base,
		Project:               wire.Project,
		Service:               wire.Service,
		Runtime:               runtime,
		CreatedUnix:           wire.CreatedUnix,
		SourceDigest:          source,
		EffectiveDigest:       effective,
		ExecutionDigest:       execution,
		PredecessorWorkloadID: wire.PredecessorWorkloadID,
		Artifacts:             artifacts,
	}

	return manifest, validManifest(manifest)
}

func parseManifestIdentifiers(wire manifestWire) (Identifier, Identifier, Identifier, bool) {
	operation, operationValid := parseIdentifier(wire.OperationToken)
	transaction, transactionValid := parseIdentifier(wire.TransactionID)
	base, baseValid := parseIdentifier(wire.BaseTransactionID)

	return operation, transaction, base, operationValid && transactionValid && baseValid
}

func parseManifestDigests(wire manifestWire) (domain.Digest, domain.Digest, domain.Digest, bool) {
	source, sourceErr := domain.ParseDigest(wire.SourceDigest)
	effective, effectiveErr := domain.ParseDigest(wire.EffectiveDigest)
	execution, executionErr := domain.ParseDigest(wire.ExecutionDigest)

	return source, effective, execution, sourceErr == nil && effectiveErr == nil && executionErr == nil
}

func artifactFromWire(wire artifactWire) (Artifact, bool) {
	kind, kindValid := parseMountKind(wire.Kind)
	archiveDigest, archiveErr := domain.ParseDigest(wire.ArchiveDigest)
	semanticDigest, semanticErr := domain.ParseDigest(wire.SemanticDigest)
	provenance, provenanceValid := parseOptionalDigest(wire.ProvenanceDigest)
	if !kindValid || archiveErr != nil || semanticErr != nil || !provenanceValid {
		return Artifact{}, false
	}

	return Artifact{
		Mount: domain.RuntimeMount{
			Kind: kind, Name: wire.Name, Source: wire.Source, Target: wire.Target, ReadOnly: false,
		},
		ProvenanceDigest: provenance,
		FileName:         wire.FileName,
		Inventory: Inventory{
			EntryCount: wire.EntryCount, PayloadBytes: wire.PayloadBytes, ArchiveBytes: wire.ArchiveBytes,
			ArchiveDigest: archiveDigest, SemanticDigest: semanticDigest,
		},
	}, true
}

func parseIdentifier(value string) (Identifier, bool) {
	var identifier Identifier
	if len(value) != hex.EncodedLen(len(identifier)) || value != strings.ToLower(value) {
		return identifier, false
	}

	decoded, err := hex.DecodeString(value)
	if err != nil {
		return Identifier{}, false
	}
	copy(identifier[:], decoded)

	return identifier, identifier != (Identifier{})
}

func parseOptionalDigest(value string) (domain.Digest, bool) {
	if value == "" {
		return domain.Digest{}, true
	}

	digest, err := domain.ParseDigest(value)

	return digest, err == nil && digest != (domain.Digest{})
}

func mountKindString(kind domain.MountKind) string {
	switch kind {
	case domain.MountBind:
		return bindMountName
	case domain.MountVolume:
		return volumeMountName
	default:
		return ""
	}
}

func parseMountKind(value string) (domain.MountKind, bool) {
	switch value {
	case bindMountName:
		return domain.MountBind, true
	case volumeMountName:
		return domain.MountVolume, true
	default:
		return 0, false
	}
}
