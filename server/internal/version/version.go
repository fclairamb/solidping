// Package version provides build-time version information.
package version

import (
	"fmt"
	"strings"
)

// Build-time variables set via ldflags.
//
//nolint:gochecknoglobals // These are package-level variables set at build time via ldflags
var (
	// Version is the semantic version, set via ldflags.
	Version = "dev"
	// Commit is the short git commit hash, set via ldflags.
	Commit = "unknown"
	// GitTime is the commit timestamp in ISO 8601 UTC format, set via ldflags.
	GitTime = "unknown"
	// UserAgent is the identity string used in protocol checks (HTTP User-Agent, SMTP EHLO, etc.).
	// Configurable via SP_USERAGENT environment variable.
	UserAgent = "solidping.io"
)

// Info holds all version information.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	GitTime string `json:"gitTime"`
	RunMode string `json:"runMode,omitempty"`
}

// Get returns all version information as a struct.
func Get() Info {
	return Info{
		Version: Version,
		Commit:  Commit,
		GitTime: GitTime,
	}
}

// String returns a formatted string with all version information.
func String() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Version:    %s\n", Version)
	fmt.Fprintf(&builder, "Commit:     %s\n", Commit)
	fmt.Fprintf(&builder, "Git Time:   %s\n", GitTime)

	return builder.String()
}
