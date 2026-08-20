package integrations

import (
	"context"
	"database/sql"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

// ErrFreeboxNotGranted is returned when the channels handler tries to
// run a Freebox-authenticated call against a channel that hasn't
// completed the LCD-pairing flow yet. The handler maps this to a 409
// (Conflict) so the dashboard can prompt the user to finish pairing.
var ErrFreeboxNotGranted = errors.New("freebox channel is not paired yet")

// ListLanHostsResponse is the body returned by the lan-hosts endpoint.
// Sticks to the dashboard convention: list endpoints wrap the slice in
// a `data` object so we have room to add pagination later.
type ListLanHostsResponse struct {
	Data []freebox.LanHost `json:"data"`
}

// LanHostsHandler handles `GET /api/v1/orgs/:org/integrations/freebox/:uid/lan-hosts`.
// It resolves the org + channel, asserts the channel is a granted
// Freebox connection, and proxies the LAN-browser query through to the
// underlying Freebox.
func (h *Handler) LanHostsHandler(writer http.ResponseWriter, req *http.Request) error {
	orgSlug := httpx.Param(req, "org")
	connectionUID := httpx.Param(req, "uid")

	hosts, err := h.svc.ListFreeboxLanHosts(req.Context(), orgSlug, connectionUID)
	if err != nil {
		return h.handleLanHostsError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, ListLanHostsResponse{Data: hosts})
}

// handleLanHostsError translates the LAN-hosts-specific service errors
// before delegating to the shared error mapper. Kept inline here so the
// generic handleError stays free of LAN-discovery noise.
func (h *Handler) handleLanHostsError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrFreeboxNotGranted):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
			"Freebox channel is not paired yet")
	default:
		return h.handleError(writer, request, err)
	}
}

// ListFreeboxLanHosts resolves the org + channel, validates it is a Freebox
// connection (preserving the precise 404 / 400 domain errors the HTTP layer
// relies on), then delegates the granted-check → decrypt-token → build-client
// → list chain to the shared freebox.ListLanHostsForChannel resolver. The
// freebox package's not-granted sentinel is mapped back to this handler's
// ErrFreeboxNotGranted so the endpoint keeps returning 409.
func (s *Service) ListFreeboxLanHosts(
	ctx context.Context, orgSlug, connectionUID string,
) ([]freebox.LanHost, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrOrganizationNotFound
		}

		return nil, err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrConnectionNotFound
		}

		return nil, err
	}

	if conn.OrganizationUID != org.UID {
		return nil, ErrConnectionNotFound
	}

	if conn.Type != models.ConnectionTypeFreebox {
		return nil, ErrFreeboxTypeMismatch
	}

	hosts, err := freebox.ListLanHostsForChannel(ctx, s.db, s.creds, org.UID, connectionUID)
	switch {
	case errors.Is(err, freebox.ErrFreeboxNotGranted):
		return nil, ErrFreeboxNotGranted
	case errors.Is(err, freebox.ErrFreeboxChannelNotFound):
		return nil, ErrConnectionNotFound
	case err != nil:
		return nil, err
	}

	return hosts, nil
}
