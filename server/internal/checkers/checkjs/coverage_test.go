package checkjs

import (
	"os"
	"testing"
)

// Coverage of the script children. Go registers its coverage exit hooks in
// the main package's init, which runs after this package's init: a child that
// exits from init writes no counters, and everything it ran counts as
// uncovered. When go test collects coverage (it sets GOCOVERDIR), the
// children get GOCOVERDIR and run from TestMain instead. Their counters land
// next to the parent's and go test merges them into the profile. Without
// coverage nothing changes: the children run from init, as in production.
var _ = func() bool {
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		childDeferred = true
		childExtraEnv = []string{"GOCOVERDIR=" + dir}
	}

	return true
}()

func TestMain(m *testing.M) {
	if childDeferred && os.Getenv(childEnvVar) == "1" {
		os.Exit(runChildProcess(os.Stdin, os.Stdout))
	}

	os.Exit(m.Run())
}
