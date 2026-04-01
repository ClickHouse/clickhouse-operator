package upgrade

import (
	"errors"
	"fmt"
	"slices"
)

const (
	ChannelStable = "stable"
	ChannelLTS    = "lts"

	stableReleases = 3
	ltsReleases    = 2
)

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
			if CompareVersions(latest, current) > 0 {
				return latest, true
			}
		}
	}

	return current, false
}

// getMajorUpdates returns all latest versions of supported major releases newer than current. Within selected channel.
func getMajorUpdates(releases *ReleaseData, current ClickHouseVersion, channel string) []ClickHouseVersion {
	// No major updates for specific release channel (e.g. 26.1) or empty channel.
	if channel != ChannelStable && channel != ChannelLTS {
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

			if CompareVersions(version, current) > 0 {
				majorUpdates = append(majorUpdates, version)
			}
		}
	}

	// lts releases considered stable, but has distinct versions, check for newer versions in lts channel as well
	findUpgrades(ChannelLTS)

	if channel == ChannelStable {
		findUpgrades(ChannelStable)
	}

	slices.SortFunc(majorUpdates, CompareVersions)

	return majorUpdates
}

// isOnChannel checks if the given release belongs to the specified channel.
func isOnChannel(releases *ReleaseData, release ClickHouseRelease, channel string) (bool, error) {
	switch channel {
	case "":
		return true, nil
	case ChannelStable:
		if _, ok := releases.Releases[ChannelStable][release]; ok {
			return true, nil
		}
		fallthrough // lts releases considered stable, but has distinct versions, check lts versions as well

	case ChannelLTS:
		if _, ok := releases.Releases[ChannelLTS][release]; ok {
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
