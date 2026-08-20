package auth

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

// PasskeyHandler exposes the passkey HTTP endpoints. It's a sibling of
// Handler — both share the same base.HandlerBase machinery and live on
// the same auth route prefix. We keep them separate so service.go stays
// password+TOTP-focused.
type PasskeyHandler struct {
	base.HandlerBase
	svc *PasskeyService
}

// NewPasskeyHandler wires the passkey service to HTTP.
func NewPasskeyHandler(svc *PasskeyService, base base.HandlerBase) *PasskeyHandler {
	return &PasskeyHandler{HandlerBase: base, svc: svc}
}

// passkeyFinishRegisterRequest is the wire body for register/finish.
type passkeyFinishRegisterRequest struct {
	Session    string          `json:"session"`
	Credential json.RawMessage `json:"credential"`
	Name       string          `json:"name"`
}

// passkeyLoginBeginRequest is the wire body for login/begin.
type passkeyLoginBeginRequest struct {
	Email string `json:"email"`
}

// passkeyFinishLoginRequest is the wire body for login/finish.
type passkeyFinishLoginRequest struct {
	Session    string          `json:"session"`
	Credential json.RawMessage `json:"credential"`
	Org        string          `json:"org"`
}

// passkeyRenameRequest is the wire body for the rename PATCH.
type passkeyRenameRequest struct {
	Name string `json:"name"`
}

// passkeyListResponse is the wire shape of GET /auth/passkeys.
type passkeyListResponse struct {
	Data []PasskeyInfo `json:"data"`
}

// passkeyRegisterFinishResponse wraps a single PasskeyInfo so the
// response shape matches the rest of the API ("never return a bare
// object that might later need siblings").
type passkeyRegisterFinishResponse struct {
	Passkey PasskeyInfo `json:"passkey"`
}

// RegisterBegin handles POST /api/v1/auth/passkeys/register/begin.
func (h *PasskeyHandler) RegisterBegin(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	resp, err := h.svc.BeginRegistration(req.Context(), claims.UserUID)
	if err != nil {
		return h.translatePasskeyError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// RegisterFinish handles POST /api/v1/auth/passkeys/register/finish.
func (h *PasskeyHandler) RegisterFinish(writer http.ResponseWriter, req *http.Request) error {
	if _, ok := getClaimsFromContext(req); !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	body, err := readPasskeyFinishRegister(req.Body)
	if err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if body.Session == "" || len(body.Credential) == 0 {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldBody, Message: "session and credential are required"},
		})
	}

	info, err := h.svc.FinishRegistration(req.Context(), body.Session, body.Credential, body.Name)
	if err != nil {
		return h.translatePasskeyError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, passkeyRegisterFinishResponse{Passkey: *info})
}

// LoginBegin handles POST /api/v1/auth/passkeys/login/begin (public).
func (h *PasskeyHandler) LoginBegin(writer http.ResponseWriter, req *http.Request) error {
	body, err := readPasskeyLoginBegin(req.Body)
	if err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	resp, err := h.svc.BeginLogin(req.Context(), body.Email)
	if err != nil {
		return h.translatePasskeyError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// LoginFinish handles POST /api/v1/auth/passkeys/login/finish (public).
func (h *PasskeyHandler) LoginFinish(writer http.ResponseWriter, req *http.Request) error {
	body, err := readPasskeyFinishLogin(req.Body)
	if err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if body.Session == "" || len(body.Credential) == 0 {
		return h.WriteValidationError(writer, "Validation error", []base.ValidationErrorField{
			{Name: fieldBody, Message: "session and credential are required"},
		})
	}

	authContext := Context{
		UserAgent:  req.Header.Get("User-Agent"),
		RemoteAddr: base.ExtractRemoteAddr(req),
	}

	resp, err := h.svc.FinishLogin(req.Context(), body.Session, body.Credential, body.Org, authContext)
	if err != nil {
		return h.translatePasskeyError(writer, req, err)
	}

	setAccessTokenCookie(writer, resp.AccessToken, resp.ExpiresIn)

	return h.WriteJSON(writer, http.StatusOK, resp)
}

// List handles GET /api/v1/auth/passkeys.
func (h *PasskeyHandler) List(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	infos, err := h.svc.ListPasskeys(req.Context(), claims.UserUID)
	if err != nil {
		return h.WriteInternalError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, passkeyListResponse{Data: infos})
}

// Rename handles PATCH /api/v1/auth/passkeys/:uid.
func (h *PasskeyHandler) Rename(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	uid := httpx.Param(req, "uid")
	if uid == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "passkey UID required")
	}

	var body passkeyRenameRequest
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		return h.WriteValidationError(writer, "Invalid JSON", []base.ValidationErrorField{
			{Name: fieldBody, Message: msgInvalidJSON},
		})
	}

	if err := h.svc.RenamePasskey(req.Context(), claims.UserUID, uid, body.Name); err != nil {
		return h.translatePasskeyError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"status": "ok"})
}

// Delete handles DELETE /api/v1/auth/passkeys/:uid.
func (h *PasskeyHandler) Delete(writer http.ResponseWriter, req *http.Request) error {
	claims, ok := getClaimsFromContext(req)
	if !ok {
		return h.WriteError(writer, http.StatusUnauthorized, base.ErrorCodeUnauthorized, "Authentication required")
	}

	uid := httpx.Param(req, "uid")
	if uid == "" {
		return h.WriteError(writer, http.StatusBadRequest, base.ErrorCodeValidationError, "passkey UID required")
	}

	if err := h.svc.DeletePasskey(req.Context(), claims.UserUID, uid); err != nil {
		return h.translatePasskeyError(writer, req, err)
	}

	return h.WriteJSON(writer, http.StatusOK, map[string]string{"status": "deleted"})
}

// translatePasskeyError maps service errors onto HTTP responses with
// the contractual error codes from base/base.go.
func (h *PasskeyHandler) translatePasskeyError(writer http.ResponseWriter, request *http.Request, err error) error {
	switch {
	case errors.Is(err, ErrWebAuthnNotConfigured):
		return h.WriteError(writer, http.StatusServiceUnavailable,
			base.ErrorCodeWebAuthnNotConfigured, "WebAuthn is not configured on this server")
	case errors.Is(err, ErrPasskeySessionExpired):
		return h.WriteError(writer, http.StatusUnauthorized,
			base.ErrorCodePasskeySessionExpired, "Passkey session expired — start over")
	case errors.Is(err, ErrPasskeyVerificationFailed):
		return h.WriteError(writer, http.StatusUnauthorized,
			base.ErrorCodePasskeyVerificationFailed, "Passkey verification failed")
	case errors.Is(err, ErrPasskeyNotFound):
		return h.WriteError(writer, http.StatusNotFound,
			base.ErrorCodePasskeyNotFound, "Passkey not found")
	case errors.Is(err, ErrPasskeyLastAuthMethod):
		return h.WriteError(writer, http.StatusConflict,
			base.ErrorCodePasskeyLastAuthMethod,
			"Add another passkey or set a password before removing this one")
	default:
		return h.WriteInternalError(writer, request, err)
	}
}

func readPasskeyFinishRegister(body io.Reader) (*passkeyFinishRegisterRequest, error) {
	var req passkeyFinishRegisterRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return nil, err
	}

	return &req, nil
}

func readPasskeyLoginBegin(body io.Reader) (*passkeyLoginBeginRequest, error) {
	var req passkeyLoginBeginRequest
	// Empty body is allowed — the discoverable / no-email flow.
	if body == nil {
		return &req, nil
	}

	if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}

	return &req, nil
}

func readPasskeyFinishLogin(body io.Reader) (*passkeyFinishLoginRequest, error) {
	var req passkeyFinishLoginRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return nil, err
	}

	return &req, nil
}
