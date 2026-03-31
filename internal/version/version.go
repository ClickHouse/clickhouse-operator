package version

import (
	"fmt"
	"runtime"
)

// Information about the current application build.
// These variables are set at build time using -ldflags "-X ...", and should not be changed in code.
var (
	// Version is the application version, which should be set to a valid semver string at build time.
	Version = "unknown"
	// GitCommitHash is the git commit hash of the current build, which should be set at build time.
	GitCommitHash = "unknown"
	// BuildTime is the time when the application was built, which should be set at build time.
	BuildTime = "unknown"
)

const (
	hashLength = 7
)

// BuildUserAgent builds a user agent string that includes the application version and build info.
func BuildUserAgent() string {
	hash := GitCommitHash
	if len(hash) > hashLength {
		hash = hash[:hashLength]
	}

	return fmt.Sprintf("clickhouse-operator/%s (%s/%s; %s)", Version, runtime.GOOS, runtime.GOARCH, hash)
}
