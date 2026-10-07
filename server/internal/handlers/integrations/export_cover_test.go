package integrations

import "net/http"

// HandleIdentityErrorForTest exposes handleIdentityError (which falls back to
// handleError) to the external test package.
func HandleIdentityErrorForTest(h *Handler, w http.ResponseWriter, r *http.Request, err error) error {
	return h.handleIdentityError(w, r, err)
}
