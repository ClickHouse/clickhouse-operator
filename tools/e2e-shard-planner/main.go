// Command e2e-shard-planner creates deterministic duration-aware e2e shard plans.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/onsi/ginkgo/v2/types"
)

const (
	defaultDuration     = time.Minute
	directoryPermission = 0o750
	filePermission      = 0o600
)

var logger = log.New(os.Stderr, "", 0)

func main() {
	if err := run(); err != nil {
		logger.Fatal(err)
	}
}

func run() error {
	var (
		reportPath string
		output     string
		shards     int
	)

	flag.StringVar(&reportPath, "report-path", "", "directory containing Ginkgo reports from the previous run")
	flag.StringVar(&output, "output", "", "path to write the shard plan")
	flag.IntVar(&shards, "shards", 0, "number of shards")
	flag.Parse()

	if flag.NArg() != 0 || output == "" || shards < 1 {
		return errors.New("usage: e2e-shard-planner -output <path> -shards <n> [-report-path <dir>]")
	}

	specs, err := discoverE2ESpecs()
	if err != nil {
		return fmt.Errorf("discover e2e specs: %w", err)
	}

	assignments, loads, err := createPlan(getTestTimings(reportPath, specs), shards)
	if err != nil {
		return err
	}

	if err := writeJSON(output, assignments); err != nil {
		return err
	}

	for i, load := range loads {
		logger.Printf("Estimated shard %d load: %s", i+1, time.Duration(load).String())
	}

	return nil
}

func discoverE2ESpecs() ([]string, error) {
	directory, err := os.MkdirTemp("", "e2e-specs-")
	if err != nil {
		return nil, fmt.Errorf("create e2e spec directory: %w", err)
	}
	defer func() {
		_ = os.RemoveAll(directory)
	}()

	command := exec.CommandContext(context.Background(), "go", "run", "github.com/onsi/ginkgo/v2/ginkgo",
		"--no-color", "--dry-run", "--output-dir", directory, "--json-report", "specs.json", "./test/e2e",
	)

	command.Env = append(os.Environ(), "E2E_SHARD_INDEX=", "E2E_SHARD_TOTAL=", "E2E_SHARD_PLAN=")

	commandOutput, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("run ginkgo dry run: %w\n%s", err, commandOutput)
	}

	reports, err := readGinkgoReports(filepath.Join(directory, "specs.json"))
	if err != nil {
		return nil, err
	}

	var specs []string
	for _, report := range reports {
		for _, specReport := range report.SpecReports.WithLeafNodeType(types.NodeTypeIt) {
			if specReport.State == types.SpecStatePending {
				continue
			}

			specs = append(specs, specReport.FullText())
		}
	}

	if len(specs) == 0 {
		return nil, errors.New("no e2e specs found")
	}

	slices.Sort(specs)

	return specs, nil
}

func readGinkgoReports(path string) ([]types.Report, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read Ginkgo report %s: %w", path, err)
	}

	var reports []types.Report
	if err := json.Unmarshal(data, &reports); err != nil {
		return nil, fmt.Errorf("decode Ginkgo report %s: %w", path, err)
	}

	return reports, nil
}

func getTestTimings(directory string, specs []string) map[string]int64 {
	timings := map[string]int64{}
	for _, spec := range specs {
		timings[spec] = 0
	}

	lastTimings, avg := getLastTimings(directory)

	for spec := range timings {
		if t, ok := lastTimings[spec]; ok {
			timings[spec] = t
		} else {
			timings[spec] = avg
		}
	}

	return timings
}

func getLastTimings(directory string) (map[string]int64, int64) {
	if directory == "" {
		return map[string]int64{}, int64(defaultDuration)
	}

	if _, err := os.Stat(directory); err != nil {
		logger.Printf("Stat report directory: %v", err)

		return map[string]int64{}, int64(defaultDuration)
	}

	var files []string

	err := filepath.WalkDir(filepath.Clean(directory), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read timing artifact: %w", err)
		}

		if !entry.IsDir() && entry.Name() == "ginkgo-report.json" {
			files = append(files, path)
		}

		return nil
	})
	if err != nil {
		logger.Printf("Walk report dirs: %v", err)
	}

	timings := map[string]int64{}
	count, total := int64(0), int64(0)

	for _, path := range files {
		reports, err := readGinkgoReports(path)
		if err != nil {
			logger.Printf("Read report %s: %v", path, err)
			continue
		}

		for _, report := range reports {
			for _, spec := range report.SpecReports.WithLeafNodeType(types.NodeTypeIt) {
				if spec.State != types.SpecStatePassed {
					continue
				}

				timings[spec.FullText()] = max(timings[spec.FullText()], spec.RunTime.Nanoseconds())
				total += spec.RunTime.Nanoseconds()
				count++
			}
		}
	}

	if total == 0 {
		return timings, int64(defaultDuration)
	}

	return timings, total / count
}

func createPlan(timings map[string]int64, shards int) (map[string]int, []int64, error) {
	if shards < 1 {
		return nil, nil, errors.New("shards must be at least one")
	}

	loads := make([]int64, shards)
	assignments := make(map[string]int, len(timings))
	ordered := append([]string(nil), slices.Collect(maps.Keys(timings))...)
	sort.Slice(ordered, func(i, j int) bool {
		left := timings[ordered[i]]
		right := timings[ordered[j]]

		if left != right {
			return left > right
		}

		return ordered[i] < ordered[j]
	})

	for _, spec := range ordered {
		shard := 0
		for index := 1; index < len(loads); index++ {
			if loads[index] < loads[shard] {
				shard = index
			}
		}

		assignments[spec] = shard + 1
		loads[shard] += timings[spec]
	}

	return assignments, loads, nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}

	if err := os.MkdirAll(filepath.Dir(path), directoryPermission); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}

	if err := os.WriteFile(filepath.Clean(path), append(data, '\n'), filePermission); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	return nil
}
