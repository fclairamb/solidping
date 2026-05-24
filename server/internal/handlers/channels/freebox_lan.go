package channels

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"

	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
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
func (h *Handler) LanHostsHandler(writer http.ResponseWriter, req bunrouter.Request) error {
	orgSlug := req.Param("org")
	connectionUID := req.Param("uid")

	hosts, err := h.svc.ListFreeboxLanHosts(req.Context(), orgSlug, connectionUID)
	if err != nil {
		return h.handleLanHostsError(writer, err)
	}

	return h.WriteJSON(writer, http.StatusOK, ListLanHostsResponse{Data: hosts})
}

// handleLanHostsError translates the LAN-hosts-specific service errors
// before delegating to the shared error mapper. Kept inline here so the
// generic handleError stays free of LAN-discovery noise.
func (h *Handler) handleLanHostsError(writer http.ResponseWriter, err error) error {
	switch {
	case errors.Is(err, ErrFreeboxNotGranted):
		return h.WriteError(writer, http.StatusConflict, base.ErrorCodeConflict,
			"Freebox channel is not paired yet")
	default:
		return h.handleError(writer, err)
	}
}

// ListFreeboxLanHosts loads the channel, validates it is a granted
// Freebox connection, builds an authenticated client and proxies the
// LAN-browser query. Returns ErrFreeboxNotGranted when the channel is
// missing an app_token (still in `pairing`, `denied`, or `timeout`).
func (s *Service) ListFreeboxLanHosts(
	ctx context.Context, orgSlug, connectionUID string,
) ([]freebox.LanHost, error) {
	conn, settings, appToken, err := s.loadGrantedFreeboxChannel(ctx, orgSlug, connectionUID)
	if err != nil {
		return nil, err
	}

	_ = conn // kept for future audit hooks; silenced for the linter

	client := s.freeboxClient(settings.BaseURL, settings.AppID, appToken)

	hosts, err := freebox.ListLanHosts(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("list lan hosts: %w", err)
	}

	return hosts, nil
}

// loadGrantedFreeboxChannel resolves the org + channel, validates the
// type is Freebox, and returns the decrypted app_token. Pulled out so
// future granted-only endpoints (e.g. line stats UI) can reuse it.
func (s *Service) loadGrantedFreeboxChannel(
	ctx context.Context, orgSlug, connectionUID string,
) (*models.Channel, *models.FreeboxSettings, string, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, "", ErrOrganizationNotFound
		}

		return nil, nil, "", err
	}

	conn, err := s.db.GetChannel(ctx, connectionUID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, "", ErrConnectionNotFound
		}

		return nil, nil, "", err
	}

	if conn.OrganizationUID != org.UID {
		return nil, nil, "", ErrConnectionNotFound
	}

	if conn.Type != models.ConnectionTypeFreebox {
		return nil, nil, "", ErrFreeboxTypeMismatch
	}

	settings, err := models.FreeboxSettingsFromJSONMap(conn.Settings)
	if err != nil {
		return nil, nil, "", fmt.Errorf("parse freebox settings: %w", err)
	}

	appToken, err := s.resolveFreeboxAppToken(ctx, conn)
	if err != nil {
		return nil, nil, "", err
	}

	if appToken == "" || settings.Status != models.FreeboxStatusGranted {
		return nil, nil, "", ErrFreeboxNotGranted
	}

	return conn, settings, appToken, nil
}

// resolveFreeboxAppToken decrypts the app_token stored on a Freebox
// channel. Mirrors the resolver wired in server.go for the checker —
// honors both encrypted-private and plaintext-fallback storage paths.
func (s *Service) resolveFreeboxAppToken(
	ctx context.Context, conn *models.Channel,
) (string, error) {
	if conn.SettingsPrivate != nil && *conn.SettingsPrivate != "" {
		if !s.creds.Enabled() {
			return "", fmt.Errorf("decrypt freebox app_token: %w (credentials disabled)", ErrFreeboxNotGranted)
		}

		priv, err := s.creds.DecryptForOrg(ctx, conn.OrganizationUID, *conn.SettingsPrivate)
		if err != nil {
			return "", fmt.Errorf("decrypt freebox app_token: %w", err)
		}

		if v, ok := priv["appToken"].(string); ok && v != "" {
			return v, nil
		}
	}

	// Plaintext fallback (credentials disabled): the same key lives on
	// the public Settings map.
	if v, ok := conn.Settings["appToken"].(string); ok {
		return v, nil
	}

	return "", nil
}
