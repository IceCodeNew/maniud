package imagearchive

import (
	"regexp"
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
)

var serviceNamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// ServiceName selects and validates the service name for an analyzed archive.
func (analysis Analysis) ServiceName(explicitName string) (string, error) {
	name := explicitName
	if name == "" {
		name = defaultServiceName(analysis.ComposeReference)
	}
	if !validAnalysis(analysis, name) {
		return "", ErrInvalidArchive
	}

	return name, nil
}

func validAnalysis(analysis Analysis, name string) bool {
	if !serviceNamePattern.MatchString(name) || analysis.ComposeReference == "" || analysis.Source.selector == "" {
		return false
	}
	if analysis.Identity.Origin != domain.ImageOriginDockerArchive ||
		analysis.Identity.Reference != analysis.ComposeReference || !validAnalysisDigests(analysis) {
		return false
	}

	return analysis.Identity.Platform.OS != "" && analysis.Identity.Platform.Architecture != ""
}

func validAnalysisDigests(analysis Analysis) bool {
	empty := domain.Digest{}

	return analysis.ArchiveDigest != empty && analysis.ArchiveSize > 0 && analysis.ManifestDigest != empty &&
		analysis.Identity.ReferenceDigest == analysis.ManifestDigest && analysis.Identity.PlatformManifest != empty &&
		analysis.Identity.ImageConfig != empty
}

func defaultServiceName(reference string) string {
	name := reference
	if slash := strings.LastIndexByte(name, '/'); slash >= 0 {
		name = name[slash+1:]
	}
	if tag := strings.LastIndexByte(name, ':'); tag >= 0 {
		name = name[:tag]
	}

	return name
}
