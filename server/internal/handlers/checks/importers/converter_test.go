package importers_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
)

func TestSupportedSources(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	sources := importers.SupportedSources()
	r.ElementsMatch([]string{"gatus", "betterstack", "uptime-kuma", "uptimerobot"}, sources)
}
