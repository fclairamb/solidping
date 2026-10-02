package incidentpublications

import "net/http"

// HandleErrorForTest exposes handleError to the external test package.
func HandleErrorForTest(h *Handler, w http.ResponseWriter, r *http.Request, err error) error {
	return h.handleError(w, r, err)
}

// IsUniqueViolationForTest exposes isUniqueViolation to the external test package.
func IsUniqueViolationForTest(err error) bool { return isUniqueViolation(err) }
