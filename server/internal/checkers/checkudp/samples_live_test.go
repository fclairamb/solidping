//go:build slowtests

// This file is behind the `slowtests` build tag because it sends the SHIPPED
// sample payloads to their real targets on the public internet (Google's
// resolver, the NTP pool). Excluded from `make test` and from both per-PR CI
// jobs; runs in the nightly `slowtests` workflow or via `make test-slow`.
//
// Its job is the one thing a local listener cannot prove: that the sample we
// publish in the catalog actually goes Up against the service it names. The NTP
// expectation in particular is a two-value byte class whose leap-indicator bits
// only a live server exercises.
// See wiki/testing/test-layers.md.

package checkudp

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestUDPSamplesAreUpAgainstTheirRealTargets(t *testing.T) {
	t.Parallel()

	for _, spec := range (&UDPChecker{}).GetSampleConfigs(nil) {
		t.Run(spec.Slug, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cfg := &UDPConfig{}
			r.NoError(cfg.FromMap(spec.Config))

			// Every sample must assert something about the answer — a UDP check
			// with no expectation cannot fail short of port-unreachable.
			r.True(cfg.ExpectData != "" || cfg.ExpectPattern != "",
				"sample %q sends nothing to assert on", spec.Slug)

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			result, err := (&UDPChecker{}).Execute(ctx, cfg)
			r.NoError(err)
			r.Equal(checkerdef.StatusUp, result.Status, result.Output)
		})
	}
}
