package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"time"

	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
)

const (
	versionsURL    = "https://raw.githubusercontent.com/ClickHouse/ClickHouse/master/utils/list-versions/version_date.tsv"
	tagURLFormat   = "https://hub.docker.com/v2/namespaces/clickhouse/repositories/%s/tags/%s"
	requestTimeout = 30 * time.Second
	filePermission = 0o600
)

var (
	logger = log.New(os.Stderr, "", 0)

	repositories = []string{"clickhouse-server", "clickhouse-keeper"}
	tagSuffixes  = []string{"", "-distroless"}
)

func main() {
	if err := run(); err != nil {
		logger.Fatal(err)
	}
}

func run() error {
	var output string

	flag.StringVar(&output, "output", "test/supported/versions.json", "path to write the supported versions")
	flag.Parse()

	ctx := context.Background()
	client := &http.Client{Timeout: requestTimeout}
	fetcher := &upgrade.URLFetcher{RequestTimeout: requestTimeout, HTTPClient: client, VersionsURL: versionsURL}

	releases, err := fetcher.FetchReleases(ctx)
	if err != nil {
		return fmt.Errorf("fetch releases: %w", err)
	}

	pinned, err := pinSupported(ctx, client, releases)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(pinned, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal versions: %w", err)
	}

	if err := os.WriteFile(output, append(data, '\n'), filePermission); err != nil {
		return fmt.Errorf("write versions: %w", err)
	}

	return nil
}

// pinSupported returns the newest published version of every supported release by channel, newest first.
func pinSupported(
	ctx context.Context, client *http.Client, releases map[string][]upgrade.ClickHouseVersion,
) (map[string][]string, error) {
	supported := upgrade.NewReleaseData(releases).Supported
	pinned := map[string][]string{}
	found := map[upgrade.ClickHouseRelease]bool{}

	for channel, versions := range releases {
		slices.SortFunc(versions, func(a, b upgrade.ClickHouseVersion) int {
			return upgrade.CompareVersions(b, a)
		})

		for _, version := range versions {
			if !supported[version.Release()] {
				continue
			}

			if !found[version.Release()] {
				ok, err := imagesPublished(ctx, client, version.Version())
				if err != nil {
					return nil, err
				}

				if !ok {
					logger.Printf("Skipping %s: images are not published", version.Version())
					continue
				}

				found[version.Release()] = true
				pinned[channel] = append(pinned[channel], version.Version())
			}
		}
	}

	return pinned, nil
}

func imagesPublished(ctx context.Context, client *http.Client, version string) (bool, error) {
	for _, repository := range repositories {
		for _, suffix := range tagSuffixes {
			if ok, err := tagExists(ctx, client, repository, version+suffix); err != nil || !ok {
				return false, err
			}
		}
	}

	return true, nil
}

func tagExists(ctx context.Context, client *http.Client, repository, tag string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(tagURLFormat, repository, tag), nil)
	if err != nil {
		return false, fmt.Errorf("create tag request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return false, fmt.Errorf("request tag %s:%s: %w", repository, tag, err)
	}

	defer func() {
		_ = resp.Body.Close()
	}()

	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("request tag %s:%s: unexpected status code %d", repository, tag, resp.StatusCode)
	}
}
