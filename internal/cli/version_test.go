package cli

import (
	"runtime/debug"
	"testing"
)

const testStableVersion = "1.2.3"

func TestVersionFromBuildInfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		release     string
		development string
		info        *debug.BuildInfo
		ok          bool
		want        string
	}{
		{name: "release", release: testStableVersion, want: testStableVersion},
		{
			name:        "injected development",
			development: "20260821-dirty-g0123456789ab",
			want:        "20260821-dirty-g0123456789ab",
		},
		{name: "clean", info: buildInfo(validBuildSettings()...), ok: true, want: "20260820-g0123456789ab"},
		{
			name: "dirty",
			info: buildInfo(replaceBuildSetting(validBuildSettings(), vcsModifiedKey, trueValue)...),
			ok:   true,
			want: "20260820-dirty-g0123456789ab",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := versionFromBuildInfo(test.release, test.development, test.info, test.ok); got != test.want {
				t.Fatalf("versionFromBuildInfo() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestVersionFromBuildInfoRejectsIncompleteEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		info *debug.BuildInfo
		ok   bool
	}{
		{name: "unavailable"},
		{name: "nil", info: nil, ok: true},
		{name: "absent setting", info: buildInfo(validBuildSettings()[:3]...), ok: true},
		{
			name: "duplicate",
			info: buildInfo(duplicateBuildSetting(validBuildSettings(), vcsKey, gitVCS)...),
			ok:   true,
		},
		{
			name: "other vcs",
			info: buildInfo(replaceBuildSetting(validBuildSettings(), vcsKey, "hg")...),
			ok:   true,
		},
		{
			name: "short revision",
			info: buildInfo(replaceBuildSetting(validBuildSettings(), vcsRevisionKey, "0123")...),
			ok:   true,
		},
		{
			name: "invalid revision",
			info: buildInfo(replaceBuildSetting(validBuildSettings(), vcsRevisionKey, "invalid-revision")...),
			ok:   true,
		},
		{
			name: "invalid time",
			info: buildInfo(replaceBuildSetting(validBuildSettings(), vcsTimeKey, "today")...),
			ok:   true,
		},
		{
			name: "invalid modified",
			info: buildInfo(replaceBuildSetting(validBuildSettings(), vcsModifiedKey, "unknown")...),
			ok:   true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := versionFromBuildInfo("", "", test.info, test.ok); got != unknownDevelopmentVersion {
				t.Fatalf("versionFromBuildInfo() = %q, want %q", got, unknownDevelopmentVersion)
			}
		})
	}
}

func TestValidDevelopmentVersion(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"20260821-g0123456789ab", "20260821-dirty-gabcdef012345"} {
		if !validDevelopmentVersion(value) {
			t.Fatalf("validDevelopmentVersion(%q) = false", value)
		}
	}
	for _, value := range []string{
		"", "20260230-g0123456789ab", "20260821-clean-g0123456789ab",
		"20260821-g0123", "20260821-dirty-g0123456789aZ",
	} {
		if validDevelopmentVersion(value) {
			t.Fatalf("validDevelopmentVersion(%q) = true", value)
		}
	}
}

func TestValidReleaseVersion(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"0.0.0", testStableVersion, "12.34.56"} {
		if !validReleaseVersion(value) {
			t.Fatalf("validReleaseVersion(%q) = false", value)
		}
	}
	for _, value := range []string{"", "1", "1.2", "v1.2.3", "01.2.3", "1..3", "1.2.x"} {
		if validReleaseVersion(value) {
			t.Fatalf("validReleaseVersion(%q) = true", value)
		}
	}
}

func buildInfo(settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{Settings: settings} //nolint:exhaustruct // Only VCS settings affect version selection.
}

func validBuildSettings() []debug.BuildSetting {
	return []debug.BuildSetting{
		{Key: vcsKey, Value: gitVCS},
		{Key: vcsRevisionKey, Value: "0123456789abcdef0123456789abcdef01234567"},
		{Key: vcsTimeKey, Value: "2026-08-21T01:02:03+02:00"},
		{Key: vcsModifiedKey, Value: falseValue},
	}
}

func duplicateBuildSetting(
	settings []debug.BuildSetting,
	key string,
	value string,
) []debug.BuildSetting {
	result := make([]debug.BuildSetting, len(settings), len(settings)+1)
	copy(result, settings)

	return append(result, debug.BuildSetting{Key: key, Value: value})
}

func replaceBuildSetting(
	settings []debug.BuildSetting,
	key string,
	value string,
) []debug.BuildSetting {
	result := append([]debug.BuildSetting(nil), settings...)
	for index := range result {
		if result[index].Key == key {
			result[index].Value = value

			return result
		}
	}

	return result
}
