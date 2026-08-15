package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	migrationBackupKind          = "maniud.sqlite-migration-backup"
	migrationBackupFormatVersion = 1
	migrationManifestMaxBytes    = 1024
)

type migrationBackupManifest struct {
	Kind          string `json:"kind"`
	FormatVersion int    `json:"format_version"`
	SourceSchema  int    `json:"source_schema"`
	TargetSchema  int    `json:"target_schema"`
	Artifact      string `json:"artifact"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256"`
}

type migrationBackupPlan struct {
	manifest     migrationBackupManifest
	artifactName string
	manifestName string
	content      []byte
}

func planMigrationBackup(
	databaseName string,
	sourceSchema int,
	targetSchema int,
	size int64,
	digest [sha256.Size]byte,
) (migrationBackupPlan, bool) {
	digestText := hex.EncodeToString(digest[:])
	artifactName := migrationArtifactName(databaseName, sourceSchema, targetSchema, digestText)

	manifest := migrationBackupManifest{
		Kind:          migrationBackupKind,
		FormatVersion: migrationBackupFormatVersion,
		SourceSchema:  sourceSchema,
		TargetSchema:  targetSchema,
		Artifact:      artifactName,
		Size:          size,
		SHA256:        digestText,
	}
	if !manifest.valid(databaseName) {
		return migrationBackupPlan{
			manifest: migrationBackupManifest{
				Kind:          "",
				FormatVersion: 0,
				SourceSchema:  0,
				TargetSchema:  0,
				Artifact:      "",
				Size:          0,
				SHA256:        "",
			},
			artifactName: "",
			manifestName: "",
			content:      nil,
		}, false
	}

	// migrationBackupManifest contains only JSON-supported scalar fields.
	content, _ := json.Marshal(manifest) //nolint:errchkjson // No field can make encoding fail.
	content = append(content, '\n')

	return migrationBackupPlan{
		manifest:     manifest,
		artifactName: artifactName,
		manifestName: strings.TrimSuffix(artifactName, ".sqlite") + ".manifest.json",
		content:      content,
	}, true
}

func parseMigrationBackupManifest(
	reader io.Reader,
	databaseName string,
) (migrationBackupManifest, bool) {
	var manifest migrationBackupManifest

	valid := jsonstrict.Decode(reader, migrationManifestMaxBytes, &manifest) && manifest.valid(databaseName)

	return manifest, valid
}

func (manifest migrationBackupManifest) valid(databaseName string) bool {
	if !manifest.validHeader(databaseName) {
		return false
	}

	digest, err := hex.DecodeString(manifest.SHA256)
	if err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != manifest.SHA256 {
		return false
	}

	wantArtifact := migrationArtifactName(
		databaseName,
		manifest.SourceSchema,
		manifest.TargetSchema,
		manifest.SHA256,
	)

	return manifest.Artifact == wantArtifact
}

func (manifest migrationBackupManifest) validHeader(databaseName string) bool {
	return manifest.Kind == migrationBackupKind && manifest.FormatVersion == migrationBackupFormatVersion &&
		manifest.SourceSchema > 0 && manifest.TargetSchema > manifest.SourceSchema &&
		manifest.TargetSchema-manifest.SourceSchema == 1 && manifest.Size > 0 &&
		validDatabaseBasename(databaseName)
}

func validDatabaseBasename(name string) bool {
	return name != "" && name != "." && name != string(filepath.Separator) && filepath.Base(name) == name
}

func migrationArtifactName(databaseName string, sourceSchema, targetSchema int, digest string) string {
	return fmt.Sprintf(
		"%s.schema-%d-to-%d.sha256-%s.sqlite",
		databaseName,
		sourceSchema,
		targetSchema,
		digest,
	)
}
