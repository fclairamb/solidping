package regions

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// A region's per-type verdict: a worker listing check-type-* is authoritative,
// a legacy worker (a set without any) runs the frozen legacy list, and a worker
// that reported nothing is unknown (spec 2026-11-26-02).
func TestAggregateCheckType(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	modern := &models.Worker{Capabilities: []string{"ipv4", models.CheckTypeCapability("http")}}
	legacy := &models.Worker{Capabilities: []string{"ipv4"}}
	silent := &models.Worker{}

	r.Equal(CapabilityYes, aggregateCheckType([]*models.Worker{modern}, "http"))
	r.Equal(CapabilityNo, aggregateCheckType([]*models.Worker{modern}, "tcp"))
	r.Equal(CapabilityYes, aggregateCheckType([]*models.Worker{legacy}, "tcp"))
	r.Equal(CapabilityNo, aggregateCheckType([]*models.Worker{legacy}, "futuretype"))
	r.Equal(CapabilityUnknown, aggregateCheckType([]*models.Worker{silent}, "http"))
	r.Equal(CapabilityUnknown, aggregateCheckType(nil, "http"))
	r.Equal(CapabilityYes, aggregateCheckType([]*models.Worker{legacy, modern}, "http"))

	caps := (&Service{}).capabilitiesFor([]*models.Worker{modern}, nil, true)
	r.Equal(CapabilityYes, caps[models.CheckTypeCapability("http")])
	r.Equal(CapabilityNo, caps[models.CheckTypeCapability("tcp")])
}
