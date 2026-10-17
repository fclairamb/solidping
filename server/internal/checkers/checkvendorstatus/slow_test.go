//go:build slowtests

package checkvendorstatus

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkvendorstatus/catalog"
)

// Every catalog URL must still answer the Statuspage v2 summary. A vendor that
// changes format breaks this nightly, not a customer.
func TestCatalogURLsServeAV2Summary(t *testing.T) {
	for _, entry := range catalog.All() {
		t.Run(entry.Slug, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			components, err := ListComponents(ctx, entry.URL)
			require.NoError(t, err)
			require.NotEmpty(t, components)
		})
	}
}
