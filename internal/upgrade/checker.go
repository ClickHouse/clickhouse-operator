package upgrade

import (
	"cmp"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

const (
	versionRegexpRaw     = `^v?(?P<Major>\d+)\.(?P<Minor>\d+)\.(?P<Patch>\d+)(\.(?P<Build>\d+))?-(?P<Channel>\w+)$`
	bareVersionRegexpRaw = `^(?P<Major>\d+)\.(?P<Minor>\d+)\.(?P<Patch>\d+)(\.(?P<Build>\d+))?$`
	channelStable        = "stable"
	channelLTS           = "lts"
	stableReleases       = 3
	ltsReleases          = 2
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

func compareVersions(a, b ClickHouseVersion) int {
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

// Checker is responsible for checking for available upgrades.
type Checker struct {
	updater *ReleaseUpdater
}

// NewChecker creates a new Checker.
func NewChecker(updater *ReleaseUpdater) *Checker {
	return &Checker{updater: updater}
}

// CheckResult represents the result of checking for updates.
type CheckResult struct {
	// MinorUpdate contains the latest minor upgrade if exists.
	MinorUpdate *ClickHouseVersion
	// MajorUpdates contains latest minor upgrades from each major release after current. Within selected channel.
	MajorUpdates []ClickHouseVersion
	// Outdated is true if current release is out of support.
	Outdated bool
	// OnChannel is true if current release belongs to selected channel.
	OnChannel bool
}

// CheckUpdates returns a list of ClickHouse versions that are newer than the current version and belong to the specified channel.
func (t *Checker) CheckUpdates(currentRaw string, channel string) (CheckResult, error) {
	current, err := ParseBareVersion(currentRaw)
	if err != nil {
		return CheckResult{}, fmt.Errorf("parse current version: %w", err)
	}

	releases := t.updater.GetReleasesData()
	if releases == nil {
		return CheckResult{}, errors.New("no release data loaded")
	}

	result := CheckResult{
		MajorUpdates: getMajorUpdates(releases, current, channel),
		Outdated:     !releases.Supported[current.Release()],
	}

	if minorUpdate, hasUpdate := getMinorUpdate(releases, current); hasUpdate {
		result.MinorUpdate = &minorUpdate
	}

	onChannel, err := isOnChannel(releases, current.Release(), channel)
	if err != nil {
		return CheckResult{}, err
	}

	result.OnChannel = onChannel

	return result, nil
}

// getMinorUpdate returns the latest minor upgrade for the current release if exists.
func getMinorUpdate(releases *ReleaseData, current ClickHouseVersion) (ClickHouseVersion, bool) {
	for _, vers := range releases.Releases {
		if latest, ok := vers[current.Release()]; ok {
			if compareVersions(latest, current) > 0 {
				return latest, true
			}
		}
	}

	return current, false
}

// getMajorUpdates returns all latest versions of supported major releases newer than current. Within selected channel.
func getMajorUpdates(releases *ReleaseData, current ClickHouseVersion, channel string) []ClickHouseVersion {
	// No major updates for specific release channel (e.g. 26.1) or empty channel.
	if channel != channelStable && channel != channelLTS {
		return nil
	}

	var majorUpdates []ClickHouseVersion

	findUpgrades := func(c string) {
		for release, version := range releases.Releases[c] {
			if release == current.Release() {
				continue
			}

			if !releases.Supported[release] {
				continue
			}

			if compareVersions(version, current) > 0 {
				majorUpdates = append(majorUpdates, version)
			}
		}
	}

	// lts releases considered stable, but has distinct versions, check for newer versions in lts channel as well
	findUpgrades(channelLTS)

	if channel == channelStable {
		findUpgrades(channelStable)
	}

	slices.SortFunc(majorUpdates, compareVersions)

	return majorUpdates
}

// isOnChannel checks if the given release belongs to the specified channel.
func isOnChannel(releases *ReleaseData, release ClickHouseRelease, channel string) (bool, error) {
	switch channel {
	case "":
		return true, nil
	case channelStable:
		if _, ok := releases.Releases[channelStable][release]; ok {
			return true, nil
		}
		fallthrough // lts releases considered stable, but has distinct versions, check lts versions as well

	case channelLTS:
		if _, ok := releases.Releases[channelLTS][release]; ok {
			return true, nil
		}
		return false, nil

	default:
		var channelBase ClickHouseRelease
		if _, err := fmt.Sscanf(channel, "%d.%d", &channelBase.Major, &channelBase.Minor); err != nil {
			return false, fmt.Errorf("parse release channel: %w", err)
		}

		return release == channelBase, nil
	}
}
