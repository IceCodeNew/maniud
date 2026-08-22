// Package runtimeargv converts strict Docker-compatible create and run argv
// into a runtime-neutral container configuration without running a client.
package runtimeargv

import (
	"errors"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/imageref"
)

const (
	// RuntimeDocker selects Docker CLI syntax.
	RuntimeDocker = "docker"
	// RuntimeNerdctl selects nerdctl CLI syntax.
	RuntimeNerdctl = "nerdctl"
	// RuntimePodman selects Podman CLI syntax.
	RuntimePodman = "podman"
	// OperationCreate configures a container without starting it.
	OperationCreate = "create"
	// OperationRun configures and starts a container.
	OperationRun = "run"

	dockerRuntime           = RuntimeDocker
	nerdctlRuntime          = RuntimeNerdctl
	podmanRuntime           = RuntimePodman
	createOperation         = OperationCreate
	runOperation            = OperationRun
	bridgeNetwork           = "bridge"
	linuxOS                 = "linux"
	amd64Architecture       = "amd64"
	arm64Architecture       = "arm64"
	arm64Variant            = "v8"
	runtimePrefixLength     = 2
	minimumRuntimeArguments = 3
	maximumServiceName      = 63
	maximumArgumentLength   = 4096
	platformParts           = 3
	platformField           = "platform"
	nameField               = "name"
	networkField            = "network"
	entrypointField         = "entrypoint"
	detachOption            = "--detach"
	noHealthcheckOption     = "--no-healthcheck"
	execOptionValue         = "exec"
	tmpfsReadOnlyOption     = "ro"
	tmpfsReadWriteOption    = "rw"
	tmpfsNoExecOption       = "noexec"
	tmpfsSUIDOption         = "suid"
	tmpfsNoSUIDOption       = "nosuid"
	tmpfsDeviceOption       = "dev"
	tmpfsNoDeviceOption     = "nodev"
	shortPortParts          = 2
	hostIPPortParts         = 3
	portProtocolTCP         = "tcp"
	portProtocolUDP         = "udp"
	booleanTrue             = "true"
	pullPolicyMissing       = "missing"
	shortDeviceParts        = 2
	fullDeviceParts         = 3
	executionReason         = "the option affects only command execution and was not included in Compose"
	outputReason            = "the option affects only command output and was not included in Compose"
	pullReason              = "maniud resolves an immutable image digest, so the acquisition option " +
		"was not included in Compose"
)

var (
	// ErrInvalid reports argv that cannot become executable desired state without loss.
	ErrInvalid      = errors.New("runtime arguments are invalid")
	namePattern     = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	platformPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*(?:/[a-z0-9][a-z0-9._-]*)?$`)
)

// Warning describes an accepted runtime option that was ignored or normalized.
// Reasons include values only for non-sensitive numeric options.
type Warning struct {
	Code   string `json:"code"`
	Option string `json:"option"`
	Reason string `json:"reason"`
}

// Projection is one validated runtime command awaiting immutable image proof.
type Projection struct {
	service   containerconfig.Spec
	source    imageref.Source
	platform  containerconfig.Platform
	warnings  []Warning
	envFiles  []string
	runtime   string
	operation string
}

// Source returns the normalized registry source named by the command.
func (projection Projection) Source() imageref.Source {
	return projection.source
}

// Platform returns the exact image platform selected by the command.
func (projection Projection) Platform() containerconfig.Platform {
	return projection.platform
}

// Spec returns an owned copy of the runtime-neutral configuration.
func (projection Projection) Spec() containerconfig.Spec {
	return projection.service.Clone()
}

// Name returns the generated Compose service name.
func (projection Projection) Name() string {
	return projection.service.ContainerName
}

// Warnings returns privacy-safe notices for ignored or normalized options.
func (projection Projection) Warnings() []Warning {
	return append([]Warning(nil), projection.warnings...)
}

// EnvironmentFiles returns source files that Compose must resolve under its
// repository trust boundary.
func (projection Projection) EnvironmentFiles() []string {
	return append([]string(nil), projection.envFiles...)
}

// Runtime returns the command's source runtime for provenance metadata.
func (projection Projection) Runtime() string {
	return projection.runtime
}

// Operation returns create or run for a parsed runtime command.
func (projection Projection) Operation() string {
	return projection.operation
}

// Parse validates and projects one complete runtime create/run argv. It only
// accepts fields already represented by maniud's workload and runtime ports;
// other runtime flags fail closed instead of producing unusable Compose.
func Parse(arguments []string, explicitName, workingDirectory string) (Projection, error) {
	parser, source, err := parseRuntimeArguments(arguments, workingDirectory)
	if err != nil {
		return Projection{}, ErrInvalid
	}

	return parser.finish(explicitName, source)
}

// ParseSource validates one registry source and derives a minimal Compose service.
func ParseSource(value, explicitName string) (Projection, error) {
	return parseSourceForArchitecture(value, explicitName, runtime.GOARCH)
}

func parseSourceForArchitecture(value, explicitName, nativeArchitecture string) (Projection, error) {
	source, err := imageref.Normalize(value)
	if err != nil {
		return Projection{}, ErrInvalid
	}
	name := selectedServiceName(explicitName, "", source.String())
	if !validSelectedName(explicitName, "", name) {
		return Projection{}, ErrInvalid
	}
	platform, err := selectedPlatformForArchitecture("", nativeArchitecture)
	if err != nil {
		return Projection{}, ErrInvalid
	}

	return Projection{
		service: containerconfig.Spec{
			ServiceName: name, ContainerName: name, NetworkMode: bridgeNetwork, Platform: platform,
		},
		source: source, platform: platform,
	}, nil
}

func parseRuntimeArguments(arguments []string, workingDirectory string) (argvParser, imageref.Source, error) {
	if len(arguments) < minimumRuntimeArguments || !filepath.IsAbs(workingDirectory) ||
		!validRuntime(arguments[0]) || !validOperation(arguments[1]) {
		return argvParser{}, imageref.Source{}, ErrInvalid
	}
	parser := newArgvParser(arguments[1], arguments)
	parser.workingDir = workingDirectory
	if err := parser.parseOptions(); err != nil || parser.index >= len(arguments) {
		return argvParser{}, imageref.Source{}, ErrInvalid
	}
	source, err := imageref.Normalize(arguments[parser.index])
	if err != nil {
		return argvParser{}, imageref.Source{}, ErrInvalid
	}
	parser.index++
	if !parser.setCommand(arguments[parser.index:]) {
		return argvParser{}, imageref.Source{}, ErrInvalid
	}

	return parser, source, nil
}

func selectedServiceName(explicit, runtimeName, reference string) string {
	if explicit != "" {
		return explicit
	}
	if runtimeName != "" {
		return runtimeName
	}

	return defaultServiceName(reference)
}

func validSelectedName(explicitName, runtimeName, selected string) bool {
	return validServiceName(selected) &&
		(explicitName == "" || runtimeName == "" || explicitName == runtimeName)
}

func selectedPlatform(value string) (containerconfig.Platform, error) {
	return selectedPlatformForArchitecture(value, runtime.GOARCH)
}

func selectedPlatformForArchitecture(value, nativeArchitecture string) (containerconfig.Platform, error) {
	if value == "" {
		if nativeArchitecture != amd64Architecture && nativeArchitecture != arm64Architecture {
			return containerconfig.Platform{}, ErrInvalid
		}
		platform := containerconfig.Platform{OS: linuxOS, Architecture: nativeArchitecture}
		if nativeArchitecture == arm64Architecture {
			platform.Variant = arm64Variant
		}

		return platform, nil
	}
	if !platformPattern.MatchString(value) {
		return containerconfig.Platform{}, ErrInvalid
	}
	parts := strings.Split(value, "/")
	platform := containerconfig.Platform{OS: parts[0], Architecture: parts[1]}
	if len(parts) == platformParts {
		platform.Variant = parts[2]
	}
	if !supportedPlatform(platform) {
		return containerconfig.Platform{}, ErrInvalid
	}
	if platform.Architecture == arm64Architecture && platform.Variant == "" {
		platform.Variant = arm64Variant
	}

	return platform, nil
}

func supportedPlatform(platform containerconfig.Platform) bool {
	return platform.OS == linuxOS &&
		(platform == (containerconfig.Platform{OS: linuxOS, Architecture: amd64Architecture}) ||
			platform == (containerconfig.Platform{OS: linuxOS, Architecture: arm64Architecture}) ||
			platform == (containerconfig.Platform{
				OS: linuxOS, Architecture: arm64Architecture, Variant: arm64Variant,
			}))
}

func platformString(platform containerconfig.Platform) string {
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}

	return value
}

func defaultServiceName(reference string) string {
	withoutDigest, _, _ := strings.Cut(reference, "@")
	lastSlash := strings.LastIndexByte(withoutDigest, '/')
	lastColon := strings.LastIndexByte(withoutDigest, ':')
	if lastColon > lastSlash {
		withoutDigest = withoutDigest[:lastColon]
	}
	name := withoutDigest[strings.LastIndexByte(withoutDigest, '/')+1:]

	return strings.NewReplacer("_", "-", ".", "-").Replace(name)
}

func validRuntime(value string) bool {
	return value == dockerRuntime || value == nerdctlRuntime || value == podmanRuntime
}

func validOperation(value string) bool {
	return value == createOperation || value == runOperation
}

func validServiceName(value string) bool {
	return len(value) <= maximumServiceName && namePattern.MatchString(value)
}

func validText(value string) bool {
	return value != "" && validArgument(value)
}

func validArgument(value string) bool {
	return len(value) <= maximumArgumentLength && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}
