package runtimeargv

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/IceCodeNew/maniud/containerconfig"
)

func (parser *argvParser) addVolume(value string) error {
	selected, err := parseVolume(value, parser.workingDir)
	if err != nil {
		return err
	}
	if conflictsWithMount(parser.service.Mounts, selected) {
		return ErrInvalid
	}
	if containsMount(parser.service.Mounts, selected) {
		return nil
	}
	if parser.targetUsedOutsideMounts(selected.Target) {
		return ErrInvalid
	}
	parser.service.Mounts = append(parser.service.Mounts, selected)

	return nil
}

//nolint:cyclop // Short volume syntax combines anonymous volumes and strict local bind paths.
func parseVolume(value, workingDir string) (containerconfig.Mount, error) {
	parts := strings.Split(value, ":")
	if len(parts) == 1 {
		if !validContainerPath(value) {
			return containerconfig.Mount{}, ErrInvalid
		}

		return containerconfig.Mount{Kind: containerconfig.MountVolume, Target: value}, nil
	}
	if len(parts) > 3 || parts[0] == "" || !validContainerPath(parts[1]) {
		return containerconfig.Mount{}, ErrInvalid
	}
	if len(parts) == 3 && !validVolumeModes(parts[2]) {
		return containerconfig.Mount{}, ErrInvalid
	}
	source := parts[0]
	switch {
	case filepath.IsAbs(source):
		if filepath.Clean(source) != source {
			return containerconfig.Mount{}, ErrInvalid
		}
	case strings.HasPrefix(source, ".") || strings.ContainsRune(source, '/'):
		source = filepath.Join(workingDir, source)
	default:
		return containerconfig.Mount{}, ErrInvalid
	}

	return containerconfig.Mount{
		Kind: containerconfig.MountBind, Source: source, Target: parts[1],
		ReadOnly: len(parts) == 3 && parts[2] == tmpfsReadOnlyOption,
	}, nil
}

func validContainerPath(value string) bool {
	return filepath.IsAbs(value) && !strings.HasPrefix(value, "//") && filepath.Clean(value) == value &&
		!strings.ContainsAny(value, " ,\t\r\n\x00")
}

func validVolumeModes(value string) bool {
	if value == "" {
		return false
	}
	seen := make(map[string]bool)
	for option := range strings.SplitSeq(value, ",") {
		if seen[option] {
			return false
		}
		seen[option] = true
		switch option {
		case tmpfsReadOnlyOption, tmpfsReadWriteOption:
		default:
			return false
		}
	}
	if seen[tmpfsReadOnlyOption] && seen[tmpfsReadWriteOption] {
		return false
	}

	return true
}

func conflictsWithMount(existing []containerconfig.Mount, candidate containerconfig.Mount) bool {
	for _, mount := range existing {
		if mount.Target == candidate.Target {
			return mount != candidate
		}
	}

	return false
}

func containsMount(values []containerconfig.Mount, candidate containerconfig.Mount) bool {
	return slices.Contains(values, candidate)
}

func (parser *argvParser) addMount(value string) error {
	mount, tmpfs, err := parseMount(value)
	if err != nil {
		return err
	}
	if tmpfs.Target != "" {
		if parser.targetUsedByMountsOrDevices(tmpfs.Target) {
			return ErrInvalid
		}
		if !addUniqueTmpfs(&parser.service.Tmpfs, tmpfs) {
			return ErrInvalid
		}

		return nil
	}
	if conflictsWithMount(parser.service.Mounts, mount) {
		return ErrInvalid
	}
	if containsMount(parser.service.Mounts, mount) {
		return nil
	}
	if parser.targetUsedOutsideMounts(mount.Target) {
		return ErrInvalid
	}
	parser.service.Mounts = append(parser.service.Mounts, mount)

	return nil
}

func parseMount(value string) (containerconfig.Mount, containerconfig.TmpfsMount, error) {
	fields, readOnly, err := mountFields(value)
	if err != nil {
		return containerconfig.Mount{}, containerconfig.TmpfsMount{}, err
	}
	switch fields["type"] {
	case "bind":
		if !onlyMountKeys(fields, "type", "source", "target", "read_only") ||
			!validContainerPath(fields["source"]) || !validContainerPath(fields["target"]) {
			return containerconfig.Mount{}, containerconfig.TmpfsMount{}, ErrInvalid
		}

		return containerconfig.Mount{
			Kind: containerconfig.MountBind, Source: fields["source"], Target: fields["target"], ReadOnly: readOnly,
		}, containerconfig.TmpfsMount{}, nil
	case "tmpfs":
		return parseTmpfsMount(fields, readOnly)
	default:
		return containerconfig.Mount{}, containerconfig.TmpfsMount{}, ErrInvalid
	}
}

//nolint:cyclop // Aliases and optional read-only values share one duplicate-key boundary.
func mountFields(value string) (map[string]string, bool, error) {
	fields := make(map[string]string)
	readOnly := false
	for raw := range strings.SplitSeq(value, ",") {
		name, selected, hasValue := strings.Cut(raw, "=")
		switch name {
		case "src":
			name = "source"
		case "dst", "destination":
			name = "target"
		case tmpfsReadOnlyOption, "readonly":
			name = "read_only"
		}
		if _, exists := fields[name]; exists {
			return nil, false, ErrInvalid
		}
		switch {
		case name == "read_only":
			if !hasValue || selected == booleanTrue {
				readOnly = true
			} else if selected != "false" {
				return nil, false, ErrInvalid
			}
			fields[name] = selected
		case !hasValue || !validText(selected):
			return nil, false, ErrInvalid
		default:
			fields[name] = selected
		}
	}

	return fields, readOnly, nil
}

func parseTmpfsMount(
	fields map[string]string,
	readOnly bool,
) (containerconfig.Mount, containerconfig.TmpfsMount, error) {
	if !onlyMountKeys(fields, "type", "target", "read_only", "tmpfs-size", "tmpfs-mode") ||
		!validContainerPath(fields["target"]) {
		return containerconfig.Mount{}, containerconfig.TmpfsMount{}, ErrInvalid
	}
	options := make([]string, 0, maximumTmpfsOptions)
	if readOnly {
		options = append(options, tmpfsReadOnlyOption)
	}
	if selected := fields["tmpfs-size"]; selected != "" {
		size, err := canonicalByteSize(selected, maximumSignedInteger)
		if err != nil {
			return containerconfig.Mount{}, containerconfig.TmpfsMount{}, err
		}
		options = append(options, "size="+size)
	}
	if selected := fields["tmpfs-mode"]; selected != "" {
		mode, err := canonicalTmpfsMode(selected)
		if err != nil {
			return containerconfig.Mount{}, containerconfig.TmpfsMount{}, err
		}
		options = append(options, "mode="+mode)
	}

	return containerconfig.Mount{}, containerconfig.TmpfsMount{Target: fields["target"], Options: options}, nil
}

func onlyMountKeys(fields map[string]string, expected ...string) bool {
	if len(fields) > len(expected) {
		return false
	}
	allowed := make(map[string]bool, len(expected))
	for _, name := range expected {
		allowed[name] = true
	}
	for name := range fields {
		if !allowed[name] {
			return false
		}
	}

	return true
}

//nolint:cyclop // Tmpfs access, size, and mode options have coupled duplicate constraints.
func parseTmpfs(value string) (containerconfig.TmpfsMount, error) {
	target, rawOptions, hasOptions := strings.Cut(value, ":")
	if !validContainerPath(target) {
		return containerconfig.TmpfsMount{}, ErrInvalid
	}
	if !hasOptions {
		return containerconfig.TmpfsMount{Target: target}, nil
	}
	if rawOptions == "" {
		return containerconfig.TmpfsMount{}, ErrInvalid
	}
	options := make([]string, 0, maximumTmpfsOptions)
	seen := make(map[string]bool)
	for raw := range strings.SplitSeq(rawOptions, ",") {
		name, selected, hasValue := strings.Cut(raw, "=")
		if seen[name] {
			return containerconfig.TmpfsMount{}, ErrInvalid
		}
		seen[name] = true
		switch name {
		case tmpfsReadOnlyOption, tmpfsReadWriteOption, execOptionValue, tmpfsNoExecOption,
			tmpfsSUIDOption, tmpfsNoSUIDOption,
			tmpfsDeviceOption, tmpfsNoDeviceOption:
			if hasValue || seen[oppositeAccess(name)] {
				return containerconfig.TmpfsMount{}, ErrInvalid
			}
			options = append(options, name)
		case "size":
			if !hasValue {
				return containerconfig.TmpfsMount{}, ErrInvalid
			}
			size, err := canonicalByteSize(selected, maximumSignedInteger)
			if err != nil {
				return containerconfig.TmpfsMount{}, err
			}
			options = append(options, "size="+size)
		case "mode":
			if !hasValue {
				return containerconfig.TmpfsMount{}, ErrInvalid
			}
			mode, err := canonicalTmpfsMode(selected)
			if err != nil {
				return containerconfig.TmpfsMount{}, err
			}
			options = append(options, "mode="+mode)
		default:
			return containerconfig.TmpfsMount{}, ErrInvalid
		}
	}

	return containerconfig.TmpfsMount{Target: target, Options: options}, nil
}

func oppositeAccess(value string) string {
	switch value {
	case tmpfsReadOnlyOption:
		return tmpfsReadWriteOption
	case tmpfsReadWriteOption:
		return tmpfsReadOnlyOption
	case execOptionValue:
		return tmpfsNoExecOption
	case tmpfsNoExecOption:
		return execOptionValue
	case tmpfsSUIDOption:
		return tmpfsNoSUIDOption
	case tmpfsNoSUIDOption:
		return tmpfsSUIDOption
	case tmpfsDeviceOption:
		return tmpfsNoDeviceOption
	case tmpfsNoDeviceOption:
		return tmpfsDeviceOption
	default:
		return ""
	}
}

func canonicalTmpfsMode(value string) (string, error) {
	if len(value) < 3 || len(value) > 5 || !asciiDigits(value) || strings.ContainsAny(value, "89") {
		return "", ErrInvalid
	}
	canonical := strings.TrimLeft(value, "0")
	if canonical == "" {
		canonical = "0"
	}

	return canonical, nil
}

func addUniqueTmpfs(values *[]containerconfig.TmpfsMount, value containerconfig.TmpfsMount) bool {
	for _, existing := range *values {
		if existing.Target == value.Target {
			return tmpfsEqual(existing, value)
		}
	}
	*values = append(*values, value)

	return true
}

func tmpfsEqual(first, second containerconfig.TmpfsMount) bool {
	return first.Target == second.Target && slicesEqual(first.Options, second.Options)
}

func slicesEqual(first, second []string) bool {
	return strings.Join(first, "\x00") == strings.Join(second, "\x00")
}

func (parser *argvParser) targetUsedByStorage(target string) bool {
	return parser.targetUsedByMountsOrDevices(target) || targetInTmpfs(parser.service.Tmpfs, target)
}

func (parser *argvParser) targetUsedOutsideMounts(target string) bool {
	return targetInTmpfs(parser.service.Tmpfs, target) || targetInDevices(parser.service.Devices, target)
}

func (parser *argvParser) targetUsedByMountsOrDevices(target string) bool {
	for _, selected := range parser.service.Mounts {
		if selected.Target == target {
			return true
		}
	}

	return targetInDevices(parser.service.Devices, target)
}

func targetInTmpfs(values []containerconfig.TmpfsMount, target string) bool {
	for _, value := range values {
		if value.Target == target {
			return true
		}
	}

	return false
}

func targetInDevices(values []containerconfig.DeviceMapping, target string) bool {
	for _, value := range values {
		if value.Target == target {
			return true
		}
	}

	return false
}
