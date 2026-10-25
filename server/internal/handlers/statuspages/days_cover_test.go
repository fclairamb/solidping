package statuspages

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestDaysForPeriod(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal(1, daysForPeriod(models.StatusPagePeriod24h))
	r.Equal(7, daysForPeriod(models.StatusPagePeriod7d))
	r.Equal(30, daysForPeriod(models.StatusPagePeriod30d))
	r.Equal(90, daysForPeriod(models.StatusPagePeriod90d))
	r.Equal(90, daysForPeriod(models.StatusPagePeriod("bogus")))
}
