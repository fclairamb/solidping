package incidents

// Magic-link ack tokens. The crypto lives in the leaf package
// `incidentlinks` so both this handler and the email sender can use it
// without forming an import cycle. This file just re-exports the bits the
// handler needs under the existing names — keeping the handler's call sites
// unchanged.

import (
	"github.com/fclairamb/solidping/server/internal/incidentlinks"
)

// Re-exported sentinels — the handler matches on these to render the right
// HTML page.
var (
	// ErrAckTokenMalformed mirrors incidentlinks.ErrMalformed.
	ErrAckTokenMalformed = incidentlinks.ErrMalformed
	// ErrAckTokenSignature mirrors incidentlinks.ErrSignature.
	ErrAckTokenSignature = incidentlinks.ErrSignature
	// ErrAckTokenExpired mirrors incidentlinks.ErrExpired.
	ErrAckTokenExpired = incidentlinks.ErrExpired
	// ErrAckTokenIncidentMismatch mirrors incidentlinks.ErrIncidentMismatch.
	ErrAckTokenIncidentMismatch = incidentlinks.ErrIncidentMismatch
)

// AckTokenPayload re-exports the verified payload type.
type AckTokenPayload = incidentlinks.AckTokenPayload

// VerifyAckToken delegates to incidentlinks.Verify.
func VerifyAckToken(secret []byte, expectedIncidentUID, token string) (*AckTokenPayload, error) {
	return incidentlinks.Verify(secret, expectedIncidentUID, token)
}
