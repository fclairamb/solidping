package main_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// goListTimeout bounds the `go list` shell-out so a wedged toolchain fails the
// test instead of hanging the suite.
const goListTimeout = 2 * time.Minute

// forbiddenDeps returns the packages `sp` must never link. Each one drags an execution
// client (client-go, go-ora, sarama, chromedp, grpc, a database driver…) into a
// binary whose whole job is to talk to the API and validate a manifest offline.
//
// The heavy registry is listed too, and it is the important entry: its init()
// wires checkjs.ResolveChecker through GetChecker, so merely IMPORTING it —
// from anywhere in the transitive graph — makes all 41 concrete checkers
// reachable and puts ~64 MB back. That is exactly how the CLI came to be 96 MB.
func forbiddenDeps() []string {
	return []string{
		"github.com/fclairamb/solidping/server/internal/checkers/registry",
		"github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes",
		"github.com/fclairamb/solidping/server/internal/checkers/checkoracle",
		"github.com/fclairamb/solidping/server/internal/checkers/checkbrowser",
		"github.com/fclairamb/solidping/server/internal/checkers/checkgrpc",
		"github.com/fclairamb/solidping/server/internal/checkers/checkjs",
		"github.com/fclairamb/solidping/server/internal/checkers/checkclickhouse",
		"github.com/fclairamb/solidping/server/internal/checkers/checkmongodb",
		"github.com/fclairamb/solidping/server/internal/checkers/checkkafka",
		"github.com/fclairamb/solidping/server/internal/checkers/checkdocker",
		"github.com/fclairamb/solidping/server/internal/checkers/checkprometheus",
	}
}

// TestCLILinksNoCheckerImplementation is the tripwire for the config/checker
// split. `sp checks validate` runs the server's own validators through
// internal/checkers/configregistry, which links only the light
// `check<type>/config` sub-packages; a stray import of a checker package (or of
// the heavy registry) silently triples the download every user gets, and
// nothing else would notice.
func TestCLILinksNoCheckerImplementation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), goListTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "go", "list", "-deps", ".").Output()
	require.NoError(t, err, "go list -deps must succeed")

	deps := make(map[string]struct{})
	for _, line := range strings.Split(string(out), "\n") {
		deps[strings.TrimSpace(line)] = struct{}{}
	}

	for _, forbidden := range forbiddenDeps() {
		_, linked := deps[forbidden]
		require.Falsef(t, linked,
			"sp links %s — the CLI must reach check configs only through "+
				"internal/checkers/configregistry, never a checker implementation",
			forbidden)
	}

	// Positive control: the light registry really is on the path, so a green
	// result cannot mean "the validator was removed".
	_, ok := deps["github.com/fclairamb/solidping/server/internal/checkers/configregistry"]
	require.True(t, ok, "sp must link internal/checkers/configregistry")
}
