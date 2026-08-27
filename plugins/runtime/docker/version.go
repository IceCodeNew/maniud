package docker

import (
	"strconv"
	"strings"
)

type apiVersion struct {
	major uint64
	minor uint64
}

const (
	dockerAPIMajor                  uint64 = 1
	cgroupNamespaceAPIMinimumMinor  uint64 = 41
	imageDescriptorsAPIMinimumMinor uint64 = 48
	imagePlatformAPIMinimumMinor    uint64 = 49
)

func parseAPIVersion(value string) (apiVersion, bool) {
	emptyVersion := apiVersion{major: 0, minor: 0}

	majorText, minorText, found := strings.Cut(value, ".")
	if !found || majorText == "" || minorText == "" || strings.Contains(minorText, ".") {
		return emptyVersion, false
	}

	major, majorErr := strconv.ParseUint(majorText, 10, 64)

	minor, minorErr := strconv.ParseUint(minorText, 10, 64)
	if majorErr != nil || minorErr != nil || strconv.FormatUint(major, 10) != majorText ||
		strconv.FormatUint(minor, 10) != minorText {
		return emptyVersion, false
	}

	return apiVersion{major: major, minor: minor}, true
}

func compatibleAPIVersion(serverMaximum apiVersion) (apiVersion, bool) {
	emptyVersion := apiVersion{major: 0, minor: 0}

	minimum, _ := parseAPIVersion(minimumAPIVersion)
	maximum, _ := parseAPIVersion(maximumAPIVersion)

	if serverMaximum.Less(minimum) {
		return emptyVersion, false
	}

	if maximum.Less(serverMaximum) {
		return maximum, true
	}

	return serverMaximum, true
}

func (version apiVersion) Less(other apiVersion) bool {
	return version.major < other.major || version.major == other.major && version.minor < other.minor
}

func (version apiVersion) String() string {
	return strconv.FormatUint(version.major, 10) + "." + strconv.FormatUint(version.minor, 10)
}

func (version apiVersion) atLeast(minimum apiVersion) bool {
	return !version.Less(minimum)
}

func supportsCgroupNamespace(version apiVersion) bool {
	return version.atLeast(apiVersion{major: dockerAPIMajor, minor: cgroupNamespaceAPIMinimumMinor})
}

func supportsImageDescriptors(version apiVersion) bool {
	return version.atLeast(apiVersion{major: dockerAPIMajor, minor: imageDescriptorsAPIMinimumMinor})
}

func supportsImageInspectPlatform(version apiVersion) bool {
	return version.atLeast(apiVersion{major: dockerAPIMajor, minor: imagePlatformAPIMinimumMinor})
}
