// Package imageconfig decodes the bounded OCI and Docker image configuration
// fields shared by registry and archive image sources.
package imageconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	maximumConfigurationTextBytes = 4096
	minimumHealthcheckDuration    = int64(time.Millisecond)
)

// ErrInvalid reports image configuration outside maniud's supported schema.
var ErrInvalid = errors.New("image configuration is invalid")

// Evidence is the runtime-neutral subset needed to prove and run one image.
type Evidence struct {
	Platform         domain.Platform
	OSVersion        string
	OSFeatures       []string
	DiffIDs          []domain.Digest
	User             string
	Environment      []string
	Entrypoint       []string
	Command          []string
	ExposedPorts     []domain.ExposedPort
	Volumes          []string
	WorkingDirectory string
	Labels           []string
	StopSignal       string
	Healthcheck      *domain.Healthcheck
}

// Identity adds decoded image configuration to an already verified image identity.
func (evidence Evidence) Identity(identity domain.ImageIdentity) domain.ImageIdentity {
	identity.User = evidence.User
	identity.Environment = slices.Clone(evidence.Environment)
	identity.Entrypoint = slices.Clone(evidence.Entrypoint)
	identity.Command = slices.Clone(evidence.Command)
	identity.ExposedPorts = slices.Clone(evidence.ExposedPorts)
	identity.Volumes = slices.Clone(evidence.Volumes)
	identity.WorkingDirectory = evidence.WorkingDirectory
	identity.Labels = slices.Clone(evidence.Labels)
	identity.StopSignal = evidence.StopSignal
	identity.Healthcheck = cloneHealthcheck(evidence.Healthcheck)

	return identity
}

func cloneHealthcheck(value *domain.Healthcheck) *domain.Healthcheck {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Test = slices.Clone(value.Test)
	if value.Retries != nil {
		retries := *value.Retries
		clone.Retries = &retries
	}

	return &clone
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type document struct {
	Architecture    string          `json:"architecture"`
	Author          string          `json:"author,omitempty"`
	Config          json.RawMessage `json:"config,omitempty"`
	Container       string          `json:"container,omitempty"`
	ContainerConfig json.RawMessage `json:"container_config,omitempty"`
	Created         json.RawMessage `json:"created,omitempty"`
	DockerVersion   string          `json:"docker_version,omitempty"`
	History         json.RawMessage `json:"history,omitempty"`
	OS              string          `json:"os"`
	OSFeatures      []string        `json:"os.features,omitempty"`
	OSVersion       string          `json:"os.version,omitempty"`
	RootFS          rootFS          `json:"rootfs"`
	Variant         string          `json:"variant,omitempty"`
}

type rootFS struct {
	Type    string   `json:"type"`
	DiffIDs []string `json:"diff_ids"`
}

//nolint:tagliatelle // OCI and Docker define these wire-field names.
type process struct {
	User         string              `json:"User,omitempty"`
	ExposedPorts map[string]struct{} `json:"ExposedPorts,omitempty"`
	Environment  []string            `json:"Env,omitempty"`
	Entrypoint   []string            `json:"Entrypoint,omitempty"`
	Command      []string            `json:"Cmd,omitempty"`
	Volumes      map[string]struct{} `json:"Volumes,omitempty"`
	WorkingDir   string              `json:"WorkingDir,omitempty"`
	Labels       map[string]string   `json:"Labels,omitempty"`
	StopSignal   string              `json:"StopSignal,omitempty"`
	ArgsEscaped  bool                `json:"ArgsEscaped,omitempty"`
	Healthcheck  *healthcheck        `json:"Healthcheck,omitempty"`
	OnBuild      []string            `json:"OnBuild,omitempty"`
	Shell        []string            `json:"Shell,omitempty"`
}

//nolint:tagliatelle // Docker defines these healthcheck wire-field names.
type healthcheck struct {
	Test          []string `json:"Test,omitempty"`
	Interval      int64    `json:"Interval,omitempty"`
	Timeout       int64    `json:"Timeout,omitempty"`
	StartPeriod   int64    `json:"StartPeriod,omitempty"`
	StartInterval int64    `json:"StartInterval,omitempty"`
	Retries       int      `json:"Retries,omitempty"`
}

// Decode validates one complete configuration document and returns the fields
// used by maniud. Unknown fields and duplicate keys fail closed.
//
//nolint:cyclop // Decoding keeps each bounded collection and nested object validation visible.
func Decode(raw []byte, maximumBytes int64) (Evidence, error) {
	var parsed document
	if maximumBytes <= 0 || !utf8.Valid(raw) ||
		!jsonstrict.Decode(bytes.NewReader(raw), maximumBytes, &parsed) {
		return Evidence{}, ErrInvalid
	}

	processValue, err := decodeProcess(parsed.Config)
	if err != nil {
		return Evidence{}, err
	}
	diffIDs, err := decodeDiffIDs(parsed.RootFS)
	if err != nil {
		return Evidence{}, err
	}

	exposedPorts, err := exposedPorts(processValue.ExposedPorts)
	if err != nil {
		return Evidence{}, err
	}
	healthcheckValue, valid := decodedHealthcheck(processValue.Healthcheck)
	if !valid {
		return Evidence{}, ErrInvalid
	}
	var labels []string
	if processValue.Labels != nil {
		labels = make([]string, 0, len(processValue.Labels))
		for key, value := range processValue.Labels {
			labels = append(labels, key+"="+value)
		}
		slices.Sort(labels)
	}
	var volumes []string
	if processValue.Volumes != nil {
		volumes = slices.Sorted(maps.Keys(processValue.Volumes))
		if volumes == nil {
			volumes = []string{}
		}
	}

	return Evidence{
		Platform: domain.Platform{
			OS:           parsed.OS,
			Architecture: parsed.Architecture,
			Variant:      parsed.Variant,
		},
		OSVersion: parsed.OSVersion, OSFeatures: slices.Clone(parsed.OSFeatures), DiffIDs: diffIDs,
		User: processValue.User, Environment: slices.Clone(processValue.Environment),
		Entrypoint: slices.Clone(processValue.Entrypoint), Command: slices.Clone(processValue.Command),
		ExposedPorts: exposedPorts, Volumes: volumes, WorkingDirectory: processValue.WorkingDir,
		Labels: labels, StopSignal: processValue.StopSignal, Healthcheck: healthcheckValue,
	}, nil
}

func decodeDiffIDs(value rootFS) ([]domain.Digest, error) {
	if value.Type != "layers" || value.DiffIDs == nil {
		return nil, ErrInvalid
	}

	result := make([]domain.Digest, len(value.DiffIDs))
	for index, raw := range value.DiffIDs {
		digest, err := domain.ParseDigest(raw)
		if err != nil {
			return nil, ErrInvalid
		}
		result[index] = digest
	}

	return result, nil
}

func exposedPorts(values map[string]struct{}) ([]domain.ExposedPort, error) {
	if values == nil {
		return nil, nil
	}
	ports := make([]domain.ExposedPort, 0, len(values))
	for value := range values {
		port, protocol, found := strings.Cut(value, "/")
		if !found {
			protocol = "tcp"
		}
		target, err := strconv.ParseUint(port, 10, 16)
		if err != nil || target == 0 || protocol != "tcp" && protocol != "udp" && protocol != "sctp" {
			return nil, ErrInvalid
		}
		ports = append(ports, domain.ExposedPort{TargetPort: uint16(target), Protocol: protocol})
	}
	slices.SortFunc(ports, func(left, right domain.ExposedPort) int {
		if left.TargetPort != right.TargetPort {
			return int(left.TargetPort) - int(right.TargetPort)
		}

		return strings.Compare(left.Protocol, right.Protocol)
	})

	return ports, nil
}

func decodedHealthcheck(value *healthcheck) (*domain.Healthcheck, bool) {
	if value == nil {
		return nil, true
	}
	if slices.Equal(value.Test, []string{"NONE"}) {
		return &domain.Healthcheck{Disabled: true}, true
	}
	if len(value.Test) < 2 || value.Test[0] != "CMD" && value.Test[0] != "CMD-SHELL" {
		return nil, false
	}
	result := &domain.Healthcheck{
		Test: slices.Clone(value.Test), Interval: durationString(value.Interval), Timeout: durationString(value.Timeout),
		StartPeriod: durationString(value.StartPeriod), StartInterval: durationString(value.StartInterval),
	}
	if value.Retries != 0 {
		retries := value.Retries
		result.Retries = &retries
	}

	return result, true
}

func durationString(value int64) string {
	if value == 0 {
		return ""
	}

	return time.Duration(value).String()
}

func decodeProcess(raw json.RawMessage) (process, error) {
	var value process
	if len(raw) == 0 {
		return value, nil
	}

	if !utf8.Valid(raw) || !jsonstrict.Decode(bytes.NewReader(raw), int64(len(raw)), &value) ||
		!validProcess(value) {
		return process{}, ErrInvalid
	}

	return value, nil
}

func validProcess(value process) bool {
	return validText(value.User) && validText(value.WorkingDir) && validText(value.StopSignal) &&
		!value.ArgsEscaped && validProcessCollections(value) && validHealthcheck(value.Healthcheck)
}

func validProcessCollections(value process) bool {
	return validEnvironment(value.Environment) && validArguments(value.Entrypoint) && validArguments(value.Command) &&
		validArguments(value.OnBuild) && validArguments(value.Shell) && validTextSet(value.ExposedPorts) &&
		validVolumes(value.Volumes) && validLabels(value.Labels)
}

//nolint:cyclop // Disabled and active image healthchecks have disjoint wire constraints.
func validHealthcheck(value *healthcheck) bool {
	if value == nil {
		return true
	}
	if slices.Equal(value.Test, []string{"NONE"}) {
		return value.Interval == 0 && value.Timeout == 0 && value.StartPeriod == 0 &&
			value.StartInterval == 0 && value.Retries == 0
	}

	return validArguments(value.Test) && validHealthcheckDuration(value.Interval) &&
		validHealthcheckDuration(value.Timeout) && validHealthcheckDuration(value.StartPeriod) &&
		validHealthcheckDuration(value.StartInterval) && value.Retries >= 0
}

func validHealthcheckDuration(value int64) bool {
	return value == 0 || value >= minimumHealthcheckDuration
}

func validTextSet(values map[string]struct{}) bool {
	for value := range values {
		if !validText(value) {
			return false
		}
	}

	return true
}

func validVolumes(values map[string]struct{}) bool {
	for value := range values {
		if !validText(value) || !path.IsAbs(value) || path.Clean(value) != value {
			return false
		}
	}

	return true
}

func validEnvironment(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key, _, found := strings.Cut(value, "=")
		if !found || key == "" || !validText(value) || strings.ContainsAny(key, " \t\r\n") {
			return false
		}
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
	}

	return true
}

func validLabels(values map[string]string) bool {
	for key, value := range values {
		if key == "" || !validText(key) || !validText(value) {
			return false
		}
	}

	return true
}

func validArguments(arguments []string) bool {
	for _, argument := range arguments {
		if !validText(argument) {
			return false
		}
	}

	return true
}

func validText(value string) bool {
	return len(value) <= maximumConfigurationTextBytes && utf8.ValidString(value) && strings.IndexByte(value, 0) < 0
}
