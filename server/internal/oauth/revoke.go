package oauth

import (
	"log/slog"
	"net/http"
)

// Revoke is the RFC 7009 token revocation endpoint. A client posts a
// form-encoded `token` (the refresh token, plus an optional `token_type_hint`)
// and its `client_id`; the grant backing that token is soft-deleted only when
// it is an OAuth refresh grant bound to the same client (see
// Service.RevokeGrant).
//
// Per RFC 7009 §2.2 the response is ALWAYS 200 with an empty body — for
// unknown, expired, already-revoked, other-client, or wrong-type tokens alike —
// so the endpoint can't be used as an oracle to probe token validity. It uses
// the same client-authentication model as /token: client_id binding, no
// client-secret verification.
func (h *Handler) Revoke(writer http.ResponseWriter, req *http.Request) error {
	if err := req.ParseForm(); err != nil {
		// A malformed body is the single RFC 7009 error case (invalid_request);
		// it reveals nothing about any token.
		return h.writeTokenError(writer, http.StatusBadRequest, ErrInvalidRequest, "malformed form body")
	}

	token := req.Form.Get("token")
	clientID := req.Form.Get(paramClientID)

	// Best-effort revocation. Any "nothing to do" outcome is indistinguishable
	// from a successful revoke by design. A genuine backend error is logged but
	// still answered 200 so the response can't leak whether the token existed.
	if _, err := h.svc.RevokeGrant(req.Context(), token, clientID); err != nil {
		slog.ErrorContext(req.Context(), "OAuth token revocation failed", "error", err)
	}

	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Pragma", "no-cache")
	writer.WriteHeader(http.StatusOK)

	return nil
}
