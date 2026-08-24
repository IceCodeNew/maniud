package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	commandargv "github.com/IceCodeNew/maniud/argv"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const (
	recommendedLoopbackAddress  = "127.0.0.1"
	recommendedLocaltimePath    = "/etc/localtime"
	recommendedRestartPolicy    = "unless-stopped"
	timezoneEnvironment         = "TZ"
	protocolTCP                 = "tcp"
	protocolUDP                 = "udp"
	warningImageSettingsReview  = "image_settings_require_review"
	warningImagePortSkipped     = "image_port_not_published"
	warningImagePortReassigned  = "image_port_reassigned"
	warningLocaltimeUnavailable = "host_localtime_unavailable"
)

type generatedPortPublisher func([]domain.ExposedPort) ([]domain.PortBinding, error)

type imageRecommendationOptions struct {
	publishPorts       generatedPortPublisher
	localtimeAvailable bool
	timezone           string
	timezoneSet        bool
}

func defaultImageRecommendationOptions(environment map[string]string) imageRecommendationOptions {
	timezone, timezoneSet := environment[timezoneEnvironment]

	return imageRecommendationOptions{
		publishPorts:       reserveLoopbackPorts,
		localtimeAvailable: regularFileExists(recommendedLocaltimePath),
		timezone:           timezone,
		timezoneSet:        timezoneSet,
	}
}

func regularFileExists(path string) bool {
	information, err := os.Stat(path)
	if err != nil {
		return false
	}

	return information.Mode().IsRegular()
}

func imageSettingsReviewWarning() runtimeargv.Warning {
	return runtimeargv.Warning{
		Code:   warningImageSettingsReview,
		Option: "image",
		Reason: "review the generated file and add required credentials, URLs, " +
			"or storage paths that the image does not declare",
	}
}

func recommendImageWorkload(
	workload domain.WorkloadSpec,
	options imageRecommendationOptions,
) (domain.WorkloadSpec, []runtimeargv.Warning, error) {
	recommended := workload.Clone()
	recommended.NoNewPrivileges = true
	if recommended.Restart == "" {
		recommended.Restart = recommendedRestartPolicy
	}
	warnings := make([]runtimeargv.Warning, 0)
	if !hasMountTarget(recommended.Mounts, recommendedLocaltimePath) {
		if options.localtimeAvailable {
			recommended.Mounts = append(recommended.Mounts, domain.Mount{
				Kind: domain.MountBind, Source: recommendedLocaltimePath,
				Target: recommendedLocaltimePath, ReadOnly: true,
			})
		} else {
			warnings = append(warnings, runtimeargv.Warning{
				Code: warningLocaltimeUnavailable, Option: recommendedLocaltimePath,
				Reason: "the local file is unavailable, so maniud did not add the read-only bind mount",
			})
		}
	}
	if options.timezoneSet {
		assignment := timezoneEnvironment + "=" + options.timezone
		if !commandargv.ValidToken(assignment) {
			return domain.WorkloadSpec{}, nil, runtimeargv.ErrInvalid
		}
		recommended.Environment = appendEnvironmentIfMissing(recommended.Environment, assignment)
	}
	publishable, portWarnings := publishableImagePorts(recommended)
	warnings = append(warnings, portWarnings...)
	if len(publishable) == 0 {
		return recommended, warnings, nil
	}
	publish := options.publishPorts
	if publish == nil {
		publish = sameLoopbackPorts
	}
	ports, err := publish(publishable)
	if err != nil || !validRecommendedPorts(publishable, ports) {
		return domain.WorkloadSpec{}, nil, runtimeargv.ErrInvalid
	}
	warnings = append(warnings, reassignedImagePortWarnings(publishable, ports)...)
	recommended.Ports = append(recommended.Ports, ports...)

	return recommended, warnings, nil
}

func appendEnvironmentIfMissing(values []string, assignment string) []string {
	name, _, _ := strings.Cut(assignment, "=")
	for _, value := range values {
		existing, _, _ := strings.Cut(value, "=")
		if existing == name {
			return values
		}
	}

	return append(values, assignment)
}

func publishableImagePorts(workload domain.WorkloadSpec) ([]domain.ExposedPort, []runtimeargv.Warning) {
	publishable := make([]domain.ExposedPort, 0, len(workload.ExposedPorts))
	warnings := make([]runtimeargv.Warning, 0)
	for _, exposed := range workload.ExposedPorts {
		if exposed.Protocol != protocolTCP && exposed.Protocol != protocolUDP {
			warnings = append(warnings, runtimeargv.Warning{
				Code: warningImagePortSkipped, Option: exposedPortName(exposed),
				Reason: "the image protocol cannot be published by the selected runtime adapter",
			})

			continue
		}
		if !hasPublishedPort(workload.Ports, exposed) {
			publishable = append(publishable, exposed)
		}
	}

	return publishable, warnings
}

func reassignedImagePortWarnings(
	publishable []domain.ExposedPort,
	ports []domain.PortBinding,
) []runtimeargv.Warning {
	warnings := make([]runtimeargv.Warning, 0)
	for index, port := range ports {
		if port.PublishedPort != publishable[index].TargetPort {
			warnings = append(warnings, runtimeargv.Warning{
				Code: warningImagePortReassigned, Option: exposedPortName(publishable[index]),
				Reason: fmt.Sprintf(
					"127.0.0.1:%d was unavailable, so maniud selected 127.0.0.1:%d",
					publishable[index].TargetPort,
					port.PublishedPort,
				),
			})
		}
	}

	return warnings
}

func reserveLoopbackPorts(exposed []domain.ExposedPort) ([]domain.PortBinding, error) {
	listeners := make([]io.Closer, 0, len(exposed))
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()

	ports := make([]domain.PortBinding, len(exposed))
	for index, value := range exposed {
		published, listener, err := reserveLoopbackPort(value.Protocol, value.TargetPort)
		if err != nil {
			return nil, err
		}
		listeners = append(listeners, listener)
		ports[index] = domain.PortBinding{
			HostIP: recommendedLoopbackAddress, PublishedPort: published,
			TargetPort: value.TargetPort, Protocol: value.Protocol,
		}
	}

	return ports, nil
}

func reserveLoopbackPort(protocol string, target uint16) (uint16, io.Closer, error) {
	address := net.JoinHostPort(recommendedLoopbackAddress, strconv.FormatUint(uint64(target), 10))
	listener, err := listenLoopback(protocol, address)
	if err != nil {
		listener, err = listenLoopback(protocol, net.JoinHostPort(recommendedLoopbackAddress, "0"))
	}
	if err != nil {
		return 0, nil, err
	}

	return loopbackListenerPort(protocol, listener), listener, nil
}

func listenLoopback(protocol, address string) (io.Closer, error) {
	var config net.ListenConfig
	var listener io.Closer
	var err error
	switch protocol {
	case protocolTCP:
		listener, err = config.Listen(context.Background(), "tcp4", address)
	case protocolUDP:
		listener, err = config.ListenPacket(context.Background(), "udp4", address)
	default:
		return nil, runtimeargv.ErrInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("reserve loopback port: %w", err)
	}

	return listener, nil
}

func loopbackListenerPort(protocol string, listener io.Closer) uint16 {
	if protocol == protocolTCP {
		conn := listener.(net.Listener)       //nolint:forcetypeassert // listenLoopback returns a TCP listener.
		address := conn.Addr().(*net.TCPAddr) //nolint:forcetypeassert // The tcp4 network guarantees TCPAddr.

		return uint16(address.Port) //nolint:gosec // TCP ports fit uint16.
	}

	conn := listener.(net.PacketConn)          //nolint:forcetypeassert // listenLoopback returns a packet connection.
	address := conn.LocalAddr().(*net.UDPAddr) //nolint:forcetypeassert // The udp4 network guarantees UDPAddr.

	return uint16(address.Port) //nolint:gosec // UDP ports fit uint16.
}

func sameLoopbackPorts(exposed []domain.ExposedPort) ([]domain.PortBinding, error) {
	ports := make([]domain.PortBinding, len(exposed))
	for index, value := range exposed {
		ports[index] = domain.PortBinding{
			HostIP: recommendedLoopbackAddress, PublishedPort: value.TargetPort,
			TargetPort: value.TargetPort, Protocol: value.Protocol,
		}
	}

	return ports, nil
}

func validRecommendedPorts(exposed []domain.ExposedPort, ports []domain.PortBinding) bool {
	if len(exposed) != len(ports) {
		return false
	}
	seen := make(map[string]struct{}, len(ports))
	for index, port := range ports {
		if port.HostIP != recommendedLoopbackAddress || port.PublishedPort == 0 ||
			port.TargetPort != exposed[index].TargetPort || port.Protocol != exposed[index].Protocol {
			return false
		}
		key := fmt.Sprintf("%d/%s", port.PublishedPort, port.Protocol)
		if _, found := seen[key]; found {
			return false
		}
		seen[key] = struct{}{}
	}

	return true
}

func hasMountTarget(mounts []domain.Mount, target string) bool {
	for _, mount := range mounts {
		if mount.Target == target {
			return true
		}
	}

	return false
}

func hasPublishedPort(ports []domain.PortBinding, exposed domain.ExposedPort) bool {
	for _, port := range ports {
		if port.TargetPort == exposed.TargetPort && port.Protocol == exposed.Protocol {
			return true
		}
	}

	return false
}

func exposedPortName(port domain.ExposedPort) string {
	return fmt.Sprintf("%d/%s", port.TargetPort, port.Protocol)
}
