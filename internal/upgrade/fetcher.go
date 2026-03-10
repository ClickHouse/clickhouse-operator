package upgrade

import (
	"cmp"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"maps"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/version"
)

const (
	defaultRequestTimeout = 5 * time.Second
	defaultVersionsURL    = "https://clickhouse.com/data/version_date.tsv"

	backoffSteps  = 10
	backoffFactor = 2.0
	backoffJitter = 0.2
)

var (
	updateRetryBackoff = wait.Backoff{
		Steps:    backoffSteps,
		Duration: time.Second,
		Factor:   backoffFactor,
		Jitter:   backoffJitter,
	}
)

// Fetcher is an interface for fetching ClickHouse releases from a source.
type Fetcher interface {
	FetchReleases(ctx context.Context) (map[string][]ClickHouseVersion, error)
}

// StaticFetcher is a simple implementation of Fetcher that returns a predefined list of releases.
// Used for testing.
type StaticFetcher struct {
	Releases map[string][]ClickHouseVersion
}

var _ Fetcher = (*StaticFetcher)(nil)

// FetchReleases returns the predefined list of releases.
func (s *StaticFetcher) FetchReleases(context.Context) (map[string][]ClickHouseVersion, error) {
	return s.Releases, nil
}

// URLFetcher is an implementation of Fetcher that fetches ClickHouse releases from a URL in TSV format.
type URLFetcher struct {
	RequestTimeout time.Duration
	HTTPClient     *http.Client
	VersionsURL    string
}

var _ Fetcher = (*URLFetcher)(nil)

// NewURLFetcher creates a new URLFetcher.
func NewURLFetcher() *URLFetcher {
	return &URLFetcher{
		RequestTimeout: defaultRequestTimeout,
		HTTPClient:     &http.Client{},
		VersionsURL:    defaultVersionsURL,
	}
}

// FetchReleases queries the list of ClickHouse releases.
func (f *URLFetcher) FetchReleases(ctx context.Context) (map[string][]ClickHouseVersion, error) {
	ctx, cancel := context.WithTimeout(ctx, f.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.VersionsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create versions request: %w", err)
	}

	req.Header.Set("User-Agent", version.BuildUserAgent())

	resp, err := f.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request version from URL: %w", err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code %d", resp.StatusCode)
	}

	return parseVersions(resp.Body)
}

type releaseMap map[string]map[ClickHouseRelease]ClickHouseVersion

// ReleaseData contains preprocessed release data: all versions by channel and supported releases.
type ReleaseData struct {
	// Releases maps channel to a map from release to the latest minor version.
	Releases releaseMap
	// Supported contains the map of supported major releases.
	Supported map[ClickHouseRelease]bool
}

// ReleaseUpdater periodically fetches and updates ClickHouse release data,
// maintaining a cache of the latest preprocessed information.
type ReleaseUpdater struct {
	fetcher  Fetcher
	interval time.Duration
	l        controllerutil.Logger

	cached atomic.Pointer[ReleaseData]
}

var _ manager.Runnable = (*ReleaseUpdater)(nil)

// NewReleaseUpdater creates a new ReleaseUpdater.
func NewReleaseUpdater(fetcher Fetcher, interval time.Duration, log controllerutil.Logger) *ReleaseUpdater {
	return &ReleaseUpdater{
		fetcher:  fetcher,
		interval: interval,
		l:        log.Named("release-updater"),
	}
}

// GetReleasesData returns the latest cached release data. It may return nil if no data has been fetched yet.
func (u *ReleaseUpdater) GetReleasesData() *ReleaseData {
	return u.cached.Load()
}

// Start the release updater loop, which periodically fetches and updates release data.
// Blocks until the context is canceled. It implements the manager.Runnable interface.
func (u *ReleaseUpdater) Start(ctx context.Context) error {
	u.l.Info("Starting release updater loop")

	backoff := updateRetryBackoff
	updateTimer := time.After(0) // trigger immediate update on start

	for {
		select {
		case <-ctx.Done():
			u.l.Info("Release updater stopped")
			return nil
		case <-updateTimer:
			if u.updateReleases(ctx) {
				updateTimer = time.After(u.interval)
				backoff = updateRetryBackoff // reset backoff on success
				continue
			}

			step := backoff.Step()
			u.l.Info("Scheduling next update attempt", "backoff", step)
			updateTimer = time.After(step)
		}
	}
}

func (u *ReleaseUpdater) updateReleases(ctx context.Context) bool {
	u.l.Debug("updating releases list")

	allReleases, err := u.fetcher.FetchReleases(ctx)
	if err != nil {
		u.l.Error(err, "fetch releases")
		return false
	}

	latestReleases := filterLatestVersions(allReleases)
	supported := buildSupportedMap(latestReleases)
	u.cached.Store(&ReleaseData{
		Releases:  latestReleases,
		Supported: supported,
	})

	return true
}

func filterLatestVersions(versions map[string][]ClickHouseVersion) releaseMap {
	latest := map[string]map[ClickHouseRelease]ClickHouseVersion{}
	for channel, vers := range versions {
		latest[channel] = map[ClickHouseRelease]ClickHouseVersion{}
		for _, v := range vers {
			if s, ok := latest[channel][v.Release()]; ok {
				if compareVersions(s, v) < 0 {
					latest[channel][v.Release()] = v
				}
			} else {
				latest[channel][v.Release()] = v
			}
		}
	}

	return latest
}

// buildSupportedMap builds the supported releases map.
// Should follow the same logic as https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/utils/security-generator/generate_security.py
func buildSupportedMap(allReleases releaseMap) map[ClickHouseRelease]bool {
	supported := map[ClickHouseRelease]bool{}
	for channel, maxReleases := range map[string]int{
		channelStable: stableReleases,
		channelLTS:    ltsReleases,
	} {
		releases := allReleases[channel]

		releasesLeft := maxReleases
		for _, release := range slices.SortedFunc(maps.Keys(releases), func(i ClickHouseRelease, j ClickHouseRelease) int {
			if c := cmp.Compare(i.Major, j.Major); c != 0 {
				return -c
			}
			return -cmp.Compare(i.Minor, j.Minor)
		}) {
			supported[release] = true

			releasesLeft--
			if releasesLeft == 0 {
				break
			}
		}
	}

	return supported
}

func parseVersions(stream io.Reader) (map[string][]ClickHouseVersion, error) {
	tsvReader := csv.NewReader(stream)
	tsvReader.Comma = '\t'
	tsvReader.FieldsPerRecord = 2
	tsvReader.ReuseRecord = true

	var (
		versions  = map[string][]ClickHouseVersion{}
		tsvRecord []string
		readErr   error
	)
	for tsvRecord, readErr = tsvReader.Read(); readErr == nil; tsvRecord, readErr = tsvReader.Read() {
		if len(tsvRecord) != 2 {
			return nil, fmt.Errorf("unexpected field count: expected 2, got %d. data: %v", len(tsvRecord), tsvRecord)
		}

		chVer, channel, err := ParseVersion(tsvRecord[0])
		if err != nil {
			return nil, fmt.Errorf("parse version: %w", err)
		}

		versions[channel] = append(versions[channel], chVer)
	}

	if readErr != io.EOF {
		return nil, fmt.Errorf("parse versions: %w", readErr)
	}

	return versions, nil
}
