// Package maniud implements the private x-maniud Compose extension codec.
// It depends only on public contracts so the extension can be published later
// without moving source-loading or workload-transaction policy with it.
package maniud

import "errors"

const (
	// Key is the top-level Compose extension name owned by this codec.
	Key = "x-maniud"

	servicesField    = "services"
	runtimeField     = "runtime"
	imageSourceField = "image_source"
	maximumFields    = 2
)

// ErrInvalid reports malformed x-maniud extension data without retaining a
// rejected value.
var ErrInvalid = errors.New("x-maniud extension is invalid")

// Runtime identifies the runtime that executes a service.
type Runtime string

const (
	// RuntimeDocker selects Docker Engine.
	RuntimeDocker Runtime = "docker"
	// RuntimePodman selects Podman Libpod.
	RuntimePodman Runtime = "podman"
	// RuntimeContainerd selects the native containerd adapter.
	RuntimeContainerd Runtime = "containerd"
)

// Extension is one decoded x-maniud document.
type Extension struct {
	Services map[string]Service
}

// Service contains extension metadata for one Compose service.
type Service struct {
	Runtime      Runtime
	ArchiveProof *ArchiveProof
}

// Decode converts the normalized value stored under Key into owned typed
// metadata. Callers remain responsible for rejecting unhandled extension keys.
func Decode(value any) (Extension, error) {
	extension, valid := exactMapping(value, servicesField)
	if !valid {
		return Extension{}, ErrInvalid
	}
	rawServices, valid := extension[servicesField].(map[string]any)
	if !valid || len(rawServices) == 0 {
		return Extension{}, ErrInvalid
	}

	services := make(map[string]Service, len(rawServices))
	for name, rawService := range rawServices {
		service, err := decodeService(rawService)
		if err != nil || name == "" {
			return Extension{}, ErrInvalid
		}
		services[name] = service
	}

	return Extension{Services: services}, nil
}

// Encode returns the normalized value to store under Key.
func Encode(extension Extension) (any, error) {
	if len(extension.Services) == 0 {
		return nil, ErrInvalid
	}
	services := make(map[string]any, len(extension.Services))
	for name, service := range extension.Services {
		encoded, err := encodeService(service)
		if err != nil || name == "" {
			return nil, ErrInvalid
		}
		services[name] = encoded
	}

	return map[string]any{servicesField: services}, nil
}

func decodeService(raw any) (Service, error) {
	service, valid := raw.(map[string]any)
	if !valid || len(service) == 0 || len(service) > maximumFields {
		return Service{}, ErrInvalid
	}
	runtimeKind, runtimeFound, err := decodeRuntime(service)
	if err != nil {
		return Service{}, err
	}

	return decodeArchiveService(service, runtimeKind, runtimeFound)
}

func decodeArchiveService(service map[string]any, runtimeKind Runtime, runtimeFound bool) (Service, error) {
	rawArchive, archiveFound := service[imageSourceField]
	if !archiveFound {
		if !runtimeFound {
			return Service{}, ErrInvalid
		}

		return Service{Runtime: runtimeKind}, nil
	}
	for key := range service {
		if key != runtimeField && key != imageSourceField {
			return Service{}, ErrInvalid
		}
	}
	archive, err := decodeArchiveProof(rawArchive)
	if err != nil {
		return Service{}, err
	}

	return Service{Runtime: runtimeKind, ArchiveProof: &archive}, nil
}

func decodeRuntime(service map[string]any) (Runtime, bool, error) {
	rawRuntime, found := service[runtimeField]
	if !found {
		return RuntimeDocker, false, nil
	}
	value, valid := rawRuntime.(string)
	runtimeKind := Runtime(value)
	if !valid || !validRuntime(runtimeKind) {
		return "", false, ErrInvalid
	}

	return runtimeKind, true, nil
}

func encodeService(service Service) (map[string]any, error) {
	if !validRuntime(service.Runtime) {
		return nil, ErrInvalid
	}
	encoded := make(map[string]any, maximumFields)
	if service.Runtime != RuntimeDocker || service.ArchiveProof == nil {
		encoded[runtimeField] = string(service.Runtime)
	}
	if service.ArchiveProof != nil {
		archive, err := encodeArchiveProof(*service.ArchiveProof)
		if err != nil {
			return nil, err
		}
		encoded[imageSourceField] = archive
	}

	return encoded, nil
}

func validRuntime(value Runtime) bool {
	return value == RuntimeDocker || value == RuntimePodman || value == RuntimeContainerd
}

func exactMapping(raw any, key string) (map[string]any, bool) {
	mapping, valid := raw.(map[string]any)
	if !valid || len(mapping) != 1 {
		return nil, false
	}
	_, found := mapping[key]

	return mapping, found
}
