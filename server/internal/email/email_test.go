package email

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIncidentThreadID(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.Equal("incident.inc-123", IncidentThreadID("inc-123"))
	r.Empty(IncidentThreadID(""), "no incident, no thread")
}
