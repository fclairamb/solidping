package unsubscribe

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

// TestWritePageIsFramingProtected pins spec 2026-09-25-28 on the one-click
// page: a page whose whole purpose is one confirm button must not be
// frameable by a third party.
func TestWritePageIsFramingProtected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rec := httptest.NewRecorder()
	writePage(rec, i18n.English, http.StatusOK, "Unsubscribe", "<p>hi</p>")

	r.Equal("SAMEORIGIN", rec.Header().Get("X-Frame-Options"))
	r.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors 'self'")
}
