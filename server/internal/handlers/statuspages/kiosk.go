package statuspages

import (
	"context"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
	"github.com/fclairamb/solidping/server/internal/statuspagekiosk"
)

// KioskTokenResponse is the ONE and ONLY place a kiosk token appears in
// plaintext (spec 2026-08-29-08): the response to the mint call that created
// it. Nothing stores, logs or re-serves it afterwards — like an API key, an
// operator who loses it regenerates rather than recovers.
type KioskTokenResponse struct {
	// Token is the plaintext to paste into the TV's URL. Present only here.
	Token string `json:"token"`
	// HasKioskToken always true on a mint; carried so the dashboard can update
	// its cached page shape from this response alone.
	HasKioskToken bool `json:"hasKioskToken"`
}

// GenerateKioskToken mints (or regenerates) the page's kiosk token.
//
// Regeneration is a plain overwrite of the stored hash, which is what makes
// "regenerate" mean "the old TV stops working immediately" with no revocation
// list, no versioning and no extra column.
func (s *Service) GenerateKioskToken(
	ctx context.Context, orgSlug, identifier string,
) (KioskTokenResponse, error) {
	page, err := s.resolveStatusPage(ctx, orgSlug, identifier)
	if err != nil {
		return KioskTokenResponse{}, err
	}

	token, hash, err := statuspagekiosk.Generate()
	if err != nil {
		return KioskTokenResponse{}, err
	}

	if err := s.db.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{
		KioskTokenHash: &hash,
	}); err != nil {
		return KioskTokenResponse{}, err
	}

	return KioskTokenResponse{Token: token, HasKioskToken: true}, nil
}

// RevokeKioskToken clears the page's kiosk token. Idempotent: revoking a page
// that has none succeeds, because "there is no kiosk token for this page" is
// the state the caller asked for and reporting an error would only invite a
// retry loop.
func (s *Service) RevokeKioskToken(ctx context.Context, orgSlug, identifier string) error {
	page, err := s.resolveStatusPage(ctx, orgSlug, identifier)
	if err != nil {
		return err
	}

	empty := ""

	return s.db.UpdateStatusPage(ctx, page.UID, &models.StatusPageUpdate{KioskTokenHash: &empty})
}

// GenerateKioskToken handles POST
// /api/v1/orgs/:org/status-pages/:statusPageUid/kiosk-token.
func (h *Handler) GenerateKioskToken(writer http.ResponseWriter, req *http.Request) error {
	result, err := h.svc.GenerateKioskToken(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "statusPageUid"))
	if err != nil {
		return h.handlePageError(writer, req, err)
	}

	// The body carries a live credential: no shared cache, no browser history
	// store, ever.
	writer.Header().Set("Cache-Control", "private, no-store")

	return h.WriteJSON(writer, http.StatusCreated, result)
}

// RevokeKioskToken handles DELETE
// /api/v1/orgs/:org/status-pages/:statusPageUid/kiosk-token.
func (h *Handler) RevokeKioskToken(writer http.ResponseWriter, req *http.Request) error {
	if err := h.svc.RevokeKioskToken(
		req.Context(), httpx.Param(req, "org"), httpx.Param(req, "statusPageUid")); err != nil {
		return h.handlePageError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusNoContent, nil)
}
