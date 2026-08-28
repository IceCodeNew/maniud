package containerd

import (
	"net/netip"
	"path/filepath"
	"slices"
	"strings"

	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const maximumGeneratedHostFileBytes = 64 << 10

func appendGeneratedHostMounts(
	stateDirectory string,
	mounts []specs.Mount,
	spec domain.WorkloadSpec,
) ([]specs.Mount, error) {
	hosts, err := hostsContent(spec.Hostname, spec.ExtraHosts)
	if err != nil {
		return nil, err
	}
	files := []struct {
		name        string
		destination string
		content     []byte
	}{
		{name: "hostname", destination: "/etc/hostname", content: hostnameContent(spec.Hostname)},
		{name: "hosts", destination: "/etc/hosts", content: hosts},
		{name: "resolv.conf", destination: "/etc/resolv.conf", content: resolverContent(spec)},
	}
	result := slices.Clone(mounts)
	for _, file := range files {
		if file.content == nil {
			continue
		}
		if len(file.content) > maximumGeneratedHostFileBytes {
			return nil, ErrUnsupportedWorkload
		}
		source := filepath.Join(stateDirectory, file.name)
		if err := writePrivateFile(source, file.content); err != nil {
			return nil, err
		}
		result = append(result, specs.Mount{
			Destination: file.destination, Type: bindMountType, Source: source,
			Options: []string{bindMountType, "ro", "rprivate"},
		})
	}

	return result, nil
}

func hostnameContent(hostname string) []byte {
	if hostname == "" {
		return nil
	}

	return []byte(hostname + "\n")
}

func hostsContent(hostname string, extra []string) ([]byte, error) {
	if hostname == "" && len(extra) == 0 {
		return nil, nil
	}
	var output strings.Builder
	output.WriteString("127.0.0.1 localhost\n::1 localhost ip6-localhost ip6-loopback\n")
	if hostname != "" {
		output.WriteString("127.0.1.1 ")
		output.WriteString(hostname)
		output.WriteByte('\n')
	}
	for _, selected := range extra {
		name, address, found := strings.Cut(selected, "=")
		parsed, err := netip.ParseAddr(address)
		if !found || name == "" || err != nil || parsed.String() != address {
			return nil, ErrUnsupportedWorkload
		}
		output.WriteString(address)
		output.WriteByte(' ')
		output.WriteString(name)
		output.WriteByte('\n')
	}

	return []byte(output.String()), nil
}

func resolverContent(spec domain.WorkloadSpec) []byte {
	if len(spec.DNS)+len(spec.DNSSearch)+len(spec.DNSOptions) == 0 {
		return nil
	}
	var output strings.Builder
	for _, address := range spec.DNS {
		output.WriteString("nameserver ")
		output.WriteString(address)
		output.WriteByte('\n')
	}
	if len(spec.DNSSearch) != 0 {
		output.WriteString("search ")
		output.WriteString(strings.Join(spec.DNSSearch, " "))
		output.WriteByte('\n')
	}
	if len(spec.DNSOptions) != 0 {
		output.WriteString("options ")
		output.WriteString(strings.Join(spec.DNSOptions, " "))
		output.WriteByte('\n')
	}

	return []byte(output.String())
}

func withNetworkNamespace(namespaces []specs.LinuxNamespace, path string) ([]specs.LinuxNamespace, error) {
	result := slices.Clone(namespaces)
	for index := range result {
		if result[index].Type == specs.NetworkNamespace {
			if result[index].Path != "" {
				return nil, ErrProtocol
			}
			result[index].Path = path

			return result, nil
		}
	}

	return nil, ErrProtocol
}
