package compose

import (
	"fmt"
	"strings"

	composecodec "github.com/IceCodeNew/maniud/containerconfig/compose"
	"github.com/IceCodeNew/maniud/internal/composeext/maniud"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	composeDockerRuntime     = string(maniud.RuntimeDocker)
	composePodmanRuntime     = string(maniud.RuntimePodman)
	composeContainerdRuntime = string(maniud.RuntimeContainerd)
)

type maniudExtension struct {
	archives map[string]archiveSource
	runtimes map[string]domain.RuntimeKind
	present  bool
}

func decodeComposeExtensions(document map[string]any) (maniudExtension, bool) {
	normalized, valid := normalizedComposeExtensions(document)
	if !valid {
		return maniudExtension{}, false
	}
	if len(normalized) == 0 {
		return maniudExtension{}, true
	}
	if len(normalized) != 1 {
		return maniudExtension{}, false
	}
	raw, found := normalized[maniud.Key]
	if !found {
		return maniudExtension{}, false
	}
	decoded, err := maniud.Decode(raw)
	if err != nil {
		return maniudExtension{}, false
	}

	return internalManiudExtension(decoded)
}

func normalizedComposeExtensions(document map[string]any) (map[string]any, bool) {
	extensions := make(map[string]any)
	for name, value := range document {
		if strings.HasPrefix(strings.ToLower(name), "x-") {
			extensions[name] = value
		}
	}
	if len(extensions) == 0 {
		return nil, true
	}
	normalized, err := composecodec.NormalizeExtensions(extensions)

	return normalized, err == nil
}

func internalManiudExtension(decoded maniud.Extension) (maniudExtension, bool) {
	archives := make(map[string]archiveSource, len(decoded.Services))
	runtimes := make(map[string]domain.RuntimeKind, len(decoded.Services))
	for name, service := range decoded.Services {
		runtimeKind, valid := internalRuntime(service.Runtime)
		if !valid {
			return maniudExtension{}, false
		}
		runtimes[name] = runtimeKind
		if service.ArchiveProof != nil {
			archive, valid := archiveSourceFromProof(*service.ArchiveProof)
			if !valid {
				return maniudExtension{}, false
			}
			archives[name] = archive
		}
	}

	return maniudExtension{archives: archives, runtimes: runtimes, present: true}, true
}

func encodeManiudService(name string, service maniud.Service) (map[string]any, error) {
	value, err := maniud.Encode(maniud.Extension{Services: map[string]maniud.Service{name: service}})
	if err != nil {
		return nil, fmt.Errorf("encode x-maniud: %w", err)
	}

	return map[string]any{maniud.Key: value}, nil
}

func mustEncodeManiudRuntime(name string, runtimeKind maniud.Runtime) map[string]any {
	value, err := encodeManiudService(name, maniud.Service{Runtime: runtimeKind})
	if err != nil {
		panic("encode validated runtime provenance: " + err.Error())
	}

	return value
}

func supportsManiudProject(values map[string]any, extension maniudExtension) bool {
	if !extension.present {
		return len(values) == 0
	}
	_, found := values[maniud.Key]

	return len(values) == 1 && found
}

func internalRuntime(value maniud.Runtime) (domain.RuntimeKind, bool) {
	switch value {
	case maniud.RuntimeDocker:
		return domain.RuntimeDocker, true
	case maniud.RuntimePodman:
		return domain.RuntimePodman, true
	case maniud.RuntimeContainerd:
		return domain.RuntimeContainerd, true
	default:
		return "", false
	}
}

func extensionRuntime(value domain.RuntimeKind) maniud.Runtime {
	switch value {
	case domain.RuntimeDocker:
		return maniud.RuntimeDocker
	case domain.RuntimePodman:
		return maniud.RuntimePodman
	case domain.RuntimeContainerd:
		return maniud.RuntimeContainerd
	default:
		panic("encode validated runtime kind")
	}
}
