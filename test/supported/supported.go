package supported

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
)

//go:embed versions.json
var clickhouseJSON []byte

var (
	// Releases contains the supported ClickHouse versions grouped by channel.
	Releases map[string][]upgrade.ClickHouseVersion
	// Base ClickHouse version used in tests.
	Base upgrade.ClickHouseVersion
	// Latest is the newest supported ClickHouse version, guaranteed greater than Base.
	Latest upgrade.ClickHouseVersion
)

func init() {
	Releases = parseVersions(clickhouseJSON)

	latestLTS := slices.MaxFunc(Releases[upgrade.ChannelLTS], upgrade.CompareVersions)
	latestStable := slices.MaxFunc(Releases[upgrade.ChannelStable], upgrade.CompareVersions)

	if upgrade.CompareVersions(latestLTS, latestStable) > 0 {
		Base = latestStable
		Latest = latestLTS
	} else {
		Base = latestLTS
		Latest = latestStable
	}
}

func parseVersions(data []byte) map[string][]upgrade.ClickHouseVersion {
	var raw map[string][]string
	if err := json.Unmarshal(data, &raw); err != nil {
		panic(fmt.Sprintf("parse supported ClickHouse versions: %v", err))
	}

	releases := make(map[string][]upgrade.ClickHouseVersion, len(raw))
	for channel, versions := range raw {
		for _, value := range versions {
			version, err := upgrade.ParseBareVersion(value)
			if err != nil {
				panic(fmt.Sprintf("parse supported ClickHouse version: %v", err))
			}

			releases[channel] = append(releases[channel], version)
		}

		slices.SortFunc(releases[channel], func(a, b upgrade.ClickHouseVersion) int { return b.Compare(a) })
	}

	return releases
}
