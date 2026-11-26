package agents_test

import (
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/mod/semver"

	"github.com/fclairamb/solidping/server/internal/agents"
)

// compatWindow is the hard minimum: a compatibility shim may be removed only
// once every agent version needing it is older than this
// (wiki/conventions/agent-compatibility.md).
const compatWindow = 30 * 24 * time.Hour

var changelogHeading = regexp.MustCompile(`(?m)^## \[(\d+\.\d+\.\d+)\]\([^)]*\) \((\d{4}-\d{2}-\d{2})\)`)

// checkCompatWindow fails when the first release >= minimum is less than 30
// days older than the newest release of the changelog.
func checkCompatWindow(changelog, minimum string) error {
	var (
		newest  time.Time
		oldest  time.Time
		oldestV string
	)

	for _, m := range changelogHeading.FindAllStringSubmatch(changelog, -1) {
		released, err := time.Parse(time.DateOnly, m[2])
		if err != nil {
			return err
		}

		if released.After(newest) {
			newest = released
		}

		if semver.Compare("v"+m[1], "v"+minimum) >= 0 && (oldestV == "" || semver.Compare("v"+m[1], "v"+oldestV) < 0) {
			oldestV, oldest = m[1], released
		}
	}

	if oldestV == "" {
		return errNoRelease
	}

	if newest.Sub(oldest) < compatWindow {
		return &windowError{version: oldestV, released: oldest, newest: newest}
	}

	return nil
}

type windowError struct {
	version          string
	released, newest time.Time
}

func (e *windowError) Error() string {
	return "MinSupportedAgentVersion " + e.version + " was released " + e.released.Format(time.DateOnly) +
		", less than 30 days before the newest release " + e.newest.Format(time.DateOnly)
}

type constError string

func (e constError) Error() string { return string(e) }

const errNoRelease = constError("no changelog release at or above MinSupportedAgentVersion")

func TestMinSupportedAgentVersionRespectsCompatWindow(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../../../CHANGELOG.md")
	require.NoError(t, err)
	require.True(t, changelogHeading.Match(raw), "changelog headings must carry a date")
	require.NoError(t, checkCompatWindow(string(raw), agents.MinSupportedAgentVersion))
}

func TestCompatWindowFailsWhenMinimumIsRecent(t *testing.T) {
	t.Parallel()

	changelog := strings.Join([]string{
		"## [0.2.0](https://x/compare/v0.1.0...v0.2.0) (2026-11-20)",
		"## [0.1.0](https://x/compare/v0.0.9...v0.1.0) (2026-11-10)",
		"## [0.0.9](https://x/compare/v0.0.8...v0.0.9) (2026-09-01)",
	}, "\n")

	require.Error(t, checkCompatWindow(changelog, "0.1.0"))
	require.NoError(t, checkCompatWindow(changelog, "0.0.9"))
}

func TestVersionSupported(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.False(agents.VersionSupported("0.1.0"))
	r.False(agents.VersionSupported("v0.40.9"))
	r.True(agents.VersionSupported(agents.MinSupportedAgentVersion))
	r.True(agents.VersionSupported("0.55.0"))
	r.True(agents.VersionSupported(""), "unknown is never unsupported")
	r.True(agents.VersionSupported("dev"), "dev builds are never unsupported")
}
