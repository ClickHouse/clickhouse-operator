package upgrade

import (
	"cmp"
	"fmt"
	"regexp"
	"strconv"
)

const (
	versionRegexpRaw     = `^v?(?P<Major>\d+)\.(?P<Minor>\d+)\.(?P<Patch>\d+)(\.(?P<Build>\d+))?-(?P<Channel>\w+)$`
	bareVersionRegexpRaw = `^(?P<Major>\d+)\.(?P<Minor>\d+)\.(?P<Patch>\d+)(\.(?P<Build>\d+))?$`
)

var (
	versionRegexp = regexp.MustCompile(versionRegexpRaw)
	majorIndex    = versionRegexp.SubexpIndex("Major")
	minorIndex    = versionRegexp.SubexpIndex("Minor")
	patchIndex    = versionRegexp.SubexpIndex("Patch")
	buildIndex    = versionRegexp.SubexpIndex("Build")
	channelIndex  = versionRegexp.SubexpIndex("Channel")

	bareVersionRegexp = regexp.MustCompile(bareVersionRegexpRaw)
	bareMajorIndex    = bareVersionRegexp.SubexpIndex("Major")
	bareMinorIndex    = bareVersionRegexp.SubexpIndex("Minor")
	barePatchIndex    = bareVersionRegexp.SubexpIndex("Patch")
	bareBuildIndex    = bareVersionRegexp.SubexpIndex("Build")
)

// ClickHouseRelease represents a ClickHouse release, identified by its year and month of release (e.g. 26.1).
type ClickHouseRelease struct {
	Major int32
	Minor int32
}

// ClickHouseVersion represents a ClickHouse version.
type ClickHouseVersion struct {
	// Major is release year (the first component of the version string).
	Major int32
	// Minor is the release month within the year (the second component of the version string).
	Minor int32
	// Patch is the patch version number (the third component of the version string).
	Patch int32
	// Build is the build number (the fourth component of the version string, optional).
	Build int32
}

// Version returns the string representation of the ClickHouse version.
func (ch ClickHouseVersion) Version() string {
	if ch.Build == 0 {
		return fmt.Sprintf("%d.%d.%d", ch.Major, ch.Minor, ch.Patch)
	}

	return fmt.Sprintf("%d.%d.%d.%d", ch.Major, ch.Minor, ch.Patch, ch.Build)
}

// Release returns the ClickHouseRelease corresponding to the major and minor version of the ClickHouseVersion.
func (ch ClickHouseVersion) Release() ClickHouseRelease {
	return ClickHouseRelease{
		Major: ch.Major,
		Minor: ch.Minor,
	}
}

// Compare returns -1, 0, or 1 comparing v to others.
func (ch ClickHouseVersion) Compare(other ClickHouseVersion) int {
	return CompareVersions(ch, other)
}

// CompareVersions compares two versions with cmp package semantics.
func CompareVersions(a, b ClickHouseVersion) int {
	if c := cmp.Compare(a.Major, b.Major); c != 0 {
		return c
	}

	if c := cmp.Compare(a.Minor, b.Minor); c != 0 {
		return c
	}

	if c := cmp.Compare(a.Patch, b.Patch); c != 0 {
		return c
	}

	return cmp.Compare(a.Build, b.Build)
}

// VersionAtLeast returns true if the actual version string is >= min.
// Returns false for empty, unparsable, or unknown version strings.
func VersionAtLeast(actual string, minVersion ClickHouseVersion) bool {
	v, err := ParseBareVersion(actual)
	if err != nil {
		return false
	}

	return v.Compare(minVersion) >= 0
}

// ParseVersion parses a raw version string into a ClickHouseVersion struct.
func ParseVersion(raw string) (ClickHouseVersion, string, error) {
	submatches := versionRegexp.FindStringSubmatch(raw)
	if submatches == nil {
		return ClickHouseVersion{}, "", fmt.Errorf("invalid version format: %s", raw)
	}

	var (
		channel = submatches[channelIndex]
	)

	major, err := strconv.ParseInt(submatches[majorIndex], 10, 32)
	if err != nil {
		return ClickHouseVersion{}, "", fmt.Errorf("parse major version: %w", err)
	}

	minor, err := strconv.ParseInt(submatches[minorIndex], 10, 32)
	if err != nil {
		return ClickHouseVersion{}, "", fmt.Errorf("parse minor version: %w", err)
	}

	patch, err := strconv.ParseInt(submatches[patchIndex], 10, 32)
	if err != nil {
		return ClickHouseVersion{}, "", fmt.Errorf("parse patch version: %w", err)
	}

	var build int32
	if submatches[buildIndex] != "" {
		buildTmp, err := strconv.ParseInt(submatches[buildIndex], 10, 32)
		if err != nil {
			return ClickHouseVersion{}, "", fmt.Errorf("parse build version: %w", err)
		}

		build = int32(buildTmp)
	}

	return ClickHouseVersion{
		Major: int32(major),
		Minor: int32(minor),
		Patch: int32(patch),
		Build: build,
	}, channel, nil
}

// ParseBareVersion parses a bare numeric version string (e.g. "25.8.2.1") without a channel suffix.
// This is used for version strings returned by the version probe.
func ParseBareVersion(raw string) (ClickHouseVersion, error) {
	submatches := bareVersionRegexp.FindStringSubmatch(raw)
	if submatches == nil {
		return ClickHouseVersion{}, fmt.Errorf("invalid bare version format: %s", raw)
	}

	major, err := strconv.ParseInt(submatches[bareMajorIndex], 10, 32)
	if err != nil {
		return ClickHouseVersion{}, fmt.Errorf("parse major version: %w", err)
	}

	minor, err := strconv.ParseInt(submatches[bareMinorIndex], 10, 32)
	if err != nil {
		return ClickHouseVersion{}, fmt.Errorf("parse minor version: %w", err)
	}

	patch, err := strconv.ParseInt(submatches[barePatchIndex], 10, 32)
	if err != nil {
		return ClickHouseVersion{}, fmt.Errorf("parse patch version: %w", err)
	}

	var build int32
	if submatches[bareBuildIndex] != "" {
		buildTmp, err := strconv.ParseInt(submatches[bareBuildIndex], 10, 32)
		if err != nil {
			return ClickHouseVersion{}, fmt.Errorf("parse build version: %w", err)
		}

		build = int32(buildTmp)
	}

	return ClickHouseVersion{
		Major: int32(major),
		Minor: int32(minor),
		Patch: int32(patch),
		Build: build,
	}, nil
}
