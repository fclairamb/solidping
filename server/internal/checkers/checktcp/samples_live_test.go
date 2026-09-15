//go:build slowtests

// This file is behind the `slowtests` build tag because it dials the real
// solidping.io on the public internet and sends the SHIPPED sample payload.
// Excluded from `make test` and from both per-PR CI jobs; runs in the nightly
// `slowtests` workflow or via `make test-slow`.
// See wiki/testing/test-layers.md.

package checktcp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestTCPSpeakingSampleIsUpAgainstSolidping proves the one TCP sample that
// sends a payload really gets an HTTP status line back from our own host —
// the thing a local listener can never establish about a published sample.
func TestTCPSpeakingSampleIsUpAgainstSolidping(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	cfg := &TCPConfig{}
	r.NoError(cfg.FromMap(speakingSolidpingSample()))
	r.NotEmpty(cfg.SendData)
	r.NotEmpty(cfg.ExpectPattern)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	result, err := (&TCPChecker{}).Execute(ctx, cfg)
	r.NoError(err)
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Contains(result.Output[checkerdef.OutputKeyReceivedData], "HTTP/1.")
}
