package incidents

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

func TestCorrelationWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	short := &models.Check{Period: timeutils.Duration(30 * time.Second)}
	r.Equal(rollupMinWindow, correlationWindow(short),
		"short period should clamp to the 5-minute floor")

	long := &models.Check{Period: timeutils.Duration(10 * time.Minute)}
	r.Equal(20*time.Minute, correlationWindow(long),
		"long period should produce 2 * period")
}

func TestDerefSlug(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := "rabbit"
	r.Equal("rabbit", derefSlug(&s))
	r.Empty(derefSlug(nil))
}
