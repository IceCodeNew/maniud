package runtimeargv

import (
	"net/netip"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func parsePort(value string) (domain.PortBinding, error) {
	base, protocol, hasProtocol := strings.Cut(value, "/")
	if hasProtocol && protocol != portProtocolTCP && protocol != portProtocolUDP {
		return domain.PortBinding{}, ErrInvalid
	}
	if !hasProtocol {
		protocol = portProtocolTCP
	}
	hostIP, published, target, err := splitPortBase(base)
	if err != nil {
		return domain.PortBinding{}, err
	}
	canonicalTarget, err := parsePortNumber(target)
	if err != nil {
		return domain.PortBinding{}, err
	}
	canonicalPublished, err := parsePortNumber(published)
	if err != nil {
		return domain.PortBinding{}, err
	}
	if hostIP != "" {
		address, parseErr := netip.ParseAddr(hostIP)
		if parseErr != nil {
			return domain.PortBinding{}, ErrInvalid
		}
		hostIP = address.String()
	}

	return domain.PortBinding{
		HostIP: hostIP, PublishedPort: canonicalPublished, TargetPort: canonicalTarget, Protocol: protocol,
	}, nil
}

func splitPortBase(value string) (string, string, string, error) {
	if strings.HasPrefix(value, "[") {
		closing := strings.IndexByte(value, ']')
		if closing < 0 || closing+1 >= len(value) || value[closing+1] != ':' {
			return "", "", "", ErrInvalid
		}
		hostIP := value[1:closing]
		published, target, found := strings.Cut(value[closing+2:], ":")
		if !found || strings.Contains(target, ":") {
			return "", "", "", ErrInvalid
		}

		return hostIP, published, target, nil
	}
	parts := strings.Split(value, ":")
	switch len(parts) {
	case shortPortParts:
		return "", parts[0], parts[1], nil
	case hostIPPortParts:
		return parts[0], parts[1], parts[2], nil
	default:
		return "", "", "", ErrInvalid
	}
}

func parsePortNumber(value string) (uint16, error) {
	if !asciiDigits(value) || len(value) > 5 {
		return 0, ErrInvalid
	}
	parsed, err := strconv.ParseUint(value, 10, 16)
	if err != nil || parsed == 0 {
		return 0, ErrInvalid
	}

	return uint16(parsed), nil
}
