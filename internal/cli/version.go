package cli

import (
	"runtime/debug"
	"strings"
	"time"
)

const (
	unknownDevelopmentVersion = "development-unknown"
	shortRevisionLength       = 12
	stableVersionComponents   = 3
	requiredVCSSettings       = 4
	vcsKey                    = "vcs"
	vcsRevisionKey            = "vcs.revision"
	vcsTimeKey                = "vcs.time"
	vcsModifiedKey            = "vcs.modified"
	gitVCS                    = "git"
	falseValue                = "false"
	trueValue                 = "true"
)

// Linker metadata is immutable after process initialization.
var releaseVersion string //nolint:gochecknoglobals // The release workflow injects this value.

var developmentVersion string //nolint:gochecknoglobals // The build script injects this value.

func currentVersion() string {
	info, ok := debug.ReadBuildInfo()

	return versionFromBuildInfo(releaseVersion, developmentVersion, info, ok)
}

func versionFromBuildInfo(release, development string, info *debug.BuildInfo, ok bool) string {
	if validReleaseVersion(release) {
		return release
	}
	if validDevelopmentVersion(development) {
		return development
	}
	if !ok || info == nil {
		return unknownDevelopmentVersion
	}

	settings, valid := vcsSettings(info)
	if !valid || settings[vcsKey] != gitVCS || !validRevision(settings[vcsRevisionKey]) {
		return unknownDevelopmentVersion
	}

	committed, err := time.Parse(time.RFC3339, settings[vcsTimeKey])
	if err != nil {
		return unknownDevelopmentVersion
	}
	modified, valid := modifiedVersionSuffix(settings[vcsModifiedKey])
	if !valid {
		return unknownDevelopmentVersion
	}

	return committed.UTC().Format("20060102") + modified + "-g" + settings[vcsRevisionKey][:shortRevisionLength]
}

func vcsSettings(info *debug.BuildInfo) (map[string]string, bool) {
	settings := make(map[string]string, requiredVCSSettings)
	for _, setting := range info.Settings {
		switch setting.Key {
		case vcsKey, vcsRevisionKey, vcsTimeKey, vcsModifiedKey:
			if _, duplicate := settings[setting.Key]; duplicate {
				return nil, false
			}
			settings[setting.Key] = setting.Value
		}
	}

	return settings, len(settings) == requiredVCSSettings
}

func modifiedVersionSuffix(value string) (string, bool) {
	switch value {
	case falseValue:
		return "", true
	case trueValue:
		return "-dirty", true
	default:
		return "", false
	}
}

func validReleaseVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != stableVersionComponents {
		return false
	}
	for _, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return false
		}
		for _, character := range []byte(part) {
			if character < '0' || character > '9' {
				return false
			}
		}
	}

	return true
}

func validRevision(value string) bool {
	if len(value) < shortRevisionLength {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}

	return true
}

func validDevelopmentVersion(value string) bool {
	var revision string
	switch {
	case len(value) == len("20060102-g")+shortRevisionLength && value[len("20060102"):len("20060102-g")] == "-g":
		revision = value[len("20060102-g"):]
	case len(value) == len("20060102-dirty-g")+shortRevisionLength &&
		value[len("20060102"):len("20060102-dirty-g")] == "-dirty-g":
		revision = value[len("20060102-dirty-g"):]
	default:
		return false
	}
	_, err := time.Parse("20060102", value[:len("20060102")])

	return err == nil && validRevision(revision)
}
