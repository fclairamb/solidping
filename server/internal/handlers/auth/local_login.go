package auth

import (
	"context"
	"errors"
	"net/http"

	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
)

// ErrLocalLoginDisabled is returned by every local-credential entry point
// (password and LDAP login, registration, password reset, password change)
// while auth.local_login.enabled is false (spec 2026-11-21-02). Passkeys,
// the federated providers, API tokens, the device flow and the MCP OAuth
// server are not local login and keep working.
var ErrLocalLoginDisabled = errors.New("local login is disabled")

// localLoginDisabledMessage is the message of the 403 LOCAL_LOGIN_DISABLED.
const localLoginDisabledMessage = "Password login is disabled on this instance, sign in with SSO"

// LocalLoginEnabled reports whether local password login is enforced on right
// now. It is resolved per call (env > DB parameter > startup config), and
// fails open while no SSO provider is available.
func (s *Service) LocalLoginEnabled(ctx context.Context) bool {
	if s.fullCfg == nil {
		return true
	}

	return systemconfig.ResolveLocalLogin(ctx, s.db, s.fullCfg).Enabled
}

// requireLocalLogin returns ErrLocalLoginDisabled while local login is off.
func (s *Service) requireLocalLogin(ctx context.Context) error {
	if !s.LocalLoginEnabled(ctx) {
		return ErrLocalLoginDisabled
	}

	return nil
}

// writeLocalLoginDisabled answers the 403 LOCAL_LOGIN_DISABLED, and reports
// whether it did so: handlers call it first, before decoding the body, so a
// disabled endpoint never reveals anything about the request.
func (h *Handler) writeLocalLoginDisabled(writer http.ResponseWriter, req *http.Request) (bool, error) {
	if h.svc.LocalLoginEnabled(req.Context()) {
		return false, nil
	}

	return true, h.WriteError(writer, http.StatusForbidden, base.ErrorCodeLocalLoginDisabled, localLoginDisabledMessage)
}
