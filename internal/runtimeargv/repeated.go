package runtimeargv

import (
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const defaultDevicePermissions = "rwm"

//nolint:cyclop // Dispatch keeps each repeated runtime option bound to one typed validator.
func (parser *argvParser) addRepeated(field, value string) error {
	switch field {
	case repeatedCapAdd, repeatedCapDrop:
		return parser.addCapability(field, value)
	case repeatedDNS:
		return parser.addDNS(value)
	case repeatedDNSOption:
		if !validOptionText(value) {
			return ErrInvalid
		}
		parser.service.DNSOptions = appendUnique(parser.service.DNSOptions, value)
	case repeatedDNSSearch:
		if !validDomain(value) {
			return ErrInvalid
		}
		parser.service.DNSSearch = appendUnique(parser.service.DNSSearch, value)
	case repeatedDevice:
		return parser.addDevice(value)
	case repeatedExtraHost:
		return parser.addExtraHost(value)
	case repeatedGroup:
		return parser.addGroup(value)
	case repeatedSysctl:
		return parser.addSysctl(value)
	case repeatedTmpfs:
		return parser.addTmpfs(value)
	case repeatedUlimit:
		return parser.addUlimits(value)
	case repeatedEnvironment:
		return addKeyValue(&parser.service.Environment, value, true)
	case repeatedEnvFile:
		parser.envFiles = appendUnique(parser.envFiles, parser.absolutePath(value))
	case repeatedLabel:
		return addKeyValue(&parser.service.Labels, value, false)
	case repeatedPort:
		return parser.addPort(value)
	case repeatedSecurity:
		return parser.addSecurityOption(value)
	case repeatedVolume:
		return parser.addVolume(value)
	default:
		return ErrInvalid
	}

	return nil
}

func appendUnique(values []string, value string) []string {
	if slices.Contains(values, value) {
		return values
	}

	return append(values, value)
}

func (parser *argvParser) addCapability(field, value string) error {
	canonical, err := canonicalCapability(value)
	if err != nil {
		return err
	}
	if field == repeatedCapAdd {
		parser.service.CapAdd = appendUnique(parser.service.CapAdd, canonical)
		slices.Sort(parser.service.CapAdd)
	} else {
		parser.service.CapDrop = appendUnique(parser.service.CapDrop, canonical)
		slices.Sort(parser.service.CapDrop)
	}

	return nil
}

func (parser *argvParser) addDNS(value string) error {
	address, err := netip.ParseAddr(value)
	if err != nil {
		return ErrInvalid
	}
	parser.service.DNS = appendUnique(parser.service.DNS, address.String())

	return nil
}

func (parser *argvParser) addDevice(value string) error {
	mapping, err := parseDevice(value)
	if err != nil {
		return err
	}
	for _, existing := range parser.service.Devices {
		if existing.Target == mapping.Target {
			if existing != mapping {
				return ErrInvalid
			}

			return nil
		}
	}
	if parser.targetUsedByStorage(mapping.Target) {
		return ErrInvalid
	}
	parser.service.Devices = append(parser.service.Devices, mapping)
	slices.SortFunc(parser.service.Devices, func(first, second domain.DeviceMapping) int {
		return strings.Compare(first.Target+"\x00"+first.Source, second.Target+"\x00"+second.Source)
	})

	return nil
}

func parseDevice(value string) (domain.DeviceMapping, error) {
	parts := strings.Split(value, ":")
	mapping := domain.DeviceMapping{Permissions: defaultDevicePermissions}
	switch len(parts) {
	case 1:
		mapping.Source = parts[0]
		mapping.Target = parts[0]
	case shortDeviceParts:
		mapping.Source = parts[0]
		if validPermissions(parts[1]) {
			mapping.Target = parts[0]
			mapping.Permissions = canonicalPermissions(parts[1])
		} else {
			mapping.Target = parts[1]
		}
	case fullDeviceParts:
		mapping.Source = parts[0]
		mapping.Target = parts[1]
		mapping.Permissions = canonicalPermissions(parts[2])
	default:
		return domain.DeviceMapping{}, ErrInvalid
	}
	if !validDevicePath(mapping.Source) || !validDevicePath(mapping.Target) ||
		!validPermissions(mapping.Permissions) {
		return domain.DeviceMapping{}, ErrInvalid
	}

	return mapping, nil
}

func validDevicePath(value string) bool {
	return filepath.IsAbs(value) && !strings.HasPrefix(value, "//") && filepath.Clean(value) == value &&
		!strings.ContainsAny(value, ":\x00\r\n")
}

func validPermissions(value string) bool {
	if value == "" {
		return false
	}
	seen := ""
	for _, permission := range value {
		if !strings.ContainsRune(defaultDevicePermissions, permission) || strings.ContainsRune(seen, permission) {
			return false
		}
		seen += string(permission)
	}

	return true
}

func canonicalPermissions(value string) string {
	var canonical strings.Builder
	for _, permission := range defaultDevicePermissions {
		if strings.ContainsRune(value, permission) {
			canonical.WriteRune(permission)
		}
	}

	return canonical.String()
}

func (parser *argvParser) addExtraHost(value string) error {
	host, address, found := strings.Cut(value, "=")
	if !found {
		host, address, found = strings.Cut(value, ":")
	}
	if !found || !validHostname(host) {
		return ErrInvalid
	}
	if strings.HasPrefix(address, "[") != strings.HasSuffix(address, "]") {
		return ErrInvalid
	}
	parsed, err := netip.ParseAddr(strings.TrimSuffix(strings.TrimPrefix(address, "["), "]"))
	if err != nil {
		return ErrInvalid
	}
	entry := host + "=" + parsed.String()
	for _, existing := range parser.service.ExtraHosts {
		existingHost, _, _ := strings.Cut(existing, "=")
		if existingHost == host {
			if existing != entry {
				return ErrInvalid
			}

			return nil
		}
	}
	parser.service.ExtraHosts = append(parser.service.ExtraHosts, entry)

	return nil
}

func (parser *argvParser) addGroup(value string) error {
	canonical, err := canonicalBoundedInteger(value, 0, maximumGroupID, false)
	if err != nil {
		return err
	}
	parser.service.GroupAdd = appendUnique(parser.service.GroupAdd, canonical)

	return nil
}

func (parser *argvParser) addSysctl(value string) error {
	name, selected, found := strings.Cut(value, "=")
	if !found || !validSysctlKey(name) || !validSysctlValue(selected) || !namespacedSysctl(name) {
		return ErrInvalid
	}
	if parser.service.Sysctls == nil {
		parser.service.Sysctls = make(map[string]string)
	}
	if previous, exists := parser.service.Sysctls[name]; exists {
		if previous != selected {
			return ErrInvalid
		}

		return nil
	}
	if len(parser.service.Sysctls) >= maximumSysctls {
		return ErrInvalid
	}
	parser.service.Sysctls[name] = selected

	return nil
}

func (parser *argvParser) addTmpfs(value string) error {
	tmpfs, err := parseTmpfs(value)
	if err != nil {
		return err
	}
	if !addUniqueTmpfs(&parser.service.Tmpfs, tmpfs) {
		return ErrInvalid
	}

	return nil
}

//nolint:cyclop // Docker and Podman encode repeated ulimits differently before sharing validation.
func (parser *argvParser) addUlimits(value string) error {
	selected := []string{value}
	if parser.runtime != dockerRuntime {
		selected = strings.Split(value, ",")
	}
	for _, item := range selected {
		name, limit, found := strings.Cut(item, "=")
		if !found {
			return ErrInvalid
		}
		if parser.runtime == podmanRuntime {
			name = strings.TrimPrefix(strings.ToLower(name), "rlimit_")
		}
		if !validUlimitName(name) {
			return ErrInvalid
		}
		parsed, err := parseUlimit(name, limit)
		if err != nil {
			return err
		}
		repeated := false
		for _, previous := range parser.service.Ulimits {
			if previous.Name == name {
				if previous != parsed {
					return ErrInvalid
				}
				repeated = true

				break
			}
		}
		if !repeated {
			parser.service.Ulimits = append(parser.service.Ulimits, parsed)
		}
	}
	slices.SortFunc(parser.service.Ulimits, func(first, second domain.Ulimit) int {
		return strings.Compare(first.Name, second.Name)
	})

	return nil
}

func parseUlimit(name, value string) (domain.Ulimit, error) {
	softValue, hardValue, found := strings.Cut(value, ":")
	if !found {
		hardValue = softValue
	}
	soft, err := canonicalUlimitValue(softValue)
	if err != nil {
		return domain.Ulimit{}, err
	}
	hard, err := canonicalUlimitValue(hardValue)
	if err != nil {
		return domain.Ulimit{}, err
	}

	if soft != hard && (soft == -1 || (hard != -1 && soft > hard)) {
		return domain.Ulimit{}, ErrInvalid
	}

	return domain.Ulimit{Name: name, Soft: soft, Hard: hard}, nil
}

func canonicalUlimitValue(value string) (int64, error) {
	if value == "-1" {
		return -1, nil
	}
	canonical, err := canonicalBoundedInteger(value, 0, maximumSignedInteger, false)
	if err != nil {
		return 0, err
	}
	parsed, _ := strconv.ParseInt(canonical, 10, 64)

	return parsed, nil
}

func (parser *argvParser) addPort(value string) error {
	binding, err := parsePort(value)
	if err != nil {
		return err
	}
	if !slices.Contains(parser.service.Ports, binding) {
		parser.service.Ports = append(parser.service.Ports, binding)
	}

	return nil
}

func (parser *argvParser) addSecurityOption(value string) error {
	if value != "no-new-privileges" && value != "no-new-privileges=true" &&
		value != "no-new-privileges:true" {
		return ErrInvalid
	}
	parser.service.NoNewPrivileges = true

	return nil
}

func addKeyValue(values *[]string, value string, environment bool) error {
	name, _, _ := strings.Cut(value, "=")
	if name == "" || (environment && !validEnvironmentName(name)) || (!environment && !validOptionText(name)) {
		return ErrInvalid
	}
	for _, existing := range *values {
		existingName, _, _ := strings.Cut(existing, "=")
		if existingName == name {
			if existing != value {
				return ErrInvalid
			}

			return nil
		}
	}
	*values = append(*values, value)

	return nil
}
