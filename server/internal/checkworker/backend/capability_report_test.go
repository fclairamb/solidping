package backend

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/version"
)

// TestCapabilityReportOncePerConnection proves an agent connected to a server
// that keeps its report (reportsOnce) sends its capability set and version on
// the first claim of a connection, the set again only when it changes, and
// both again on a new connection. Against an older server it sends both on
// every claim.
func TestCapabilityReportOncePerConnection(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	caps := []string{"ipv4", "check-type-http"}
	b := &WSBackend{connectedAt: time.Now(), reportsOnce: true}

	sent, ver, connAt := b.capabilityReport(caps)
	r.Equal(caps, sent)
	r.Equal(version.Get().Version, ver)
	r.Equal(b.connectedAt, connAt)

	// claim records the report after a successful request.
	b.capsReportedAt, b.capsReported = connAt, caps

	sent, ver, _ = b.capabilityReport(caps)
	r.Nil(sent, "the same connection does not get the set twice")
	r.Empty(ver, "nor the version")

	sent, ver, _ = b.capabilityReport([]string{"ipv4"})
	r.Equal([]string{"ipv4"}, sent, "a changed set is sent again")
	r.Empty(ver)

	b.connectedAt = b.connectedAt.Add(time.Second)

	sent, ver, _ = b.capabilityReport(caps)
	r.Equal(caps, sent, "a new connection gets both again")
	r.Equal(version.Get().Version, ver)

	older := &WSBackend{connectedAt: time.Now()}
	older.capsReportedAt, older.capsReported = older.connectedAt, caps

	sent, ver, _ = older.capabilityReport(caps)
	r.Equal(caps, sent, "an older server gets the set on every claim")
	r.Equal(version.Get().Version, ver)
}
