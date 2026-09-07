package auth

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// keyAttribution is the pending-registration state key that carries the
// campaign context between Register and ConfirmRegistration.
const keyAttribution = "attribution"

// signupAttributionMaxLen bounds every stored value. Real campaign tags are a
// few dozen characters and a gclid is under 120; anything longer is not a tag,
// it is someone using the field as free storage.
const signupAttributionMaxLen = 200

// isKnownClickIDKind reports whether kind is one of the click-id parameter
// names the marketing site forwards (see its src/lib/attribution.ts): gclid,
// gbraid and wbraid from Google, msclkid from Microsoft. A kind outside the set
// is dropped together with its id: an upload needs to know which network
// minted the token, and "some string" cannot be uploaded anywhere.
func isKnownClickIDKind(kind string) bool {
	switch kind {
	case "gclid", "gbraid", "wbraid", "msclkid":
		return true
	default:
		return false
	}
}

// normalizeSignupAttribution turns whatever the client sent into the shape we
// are willing to persist: trimmed, length-capped, click-id kind whitelisted,
// and nil when nothing useful remains. CapturedAt defaults to now when the
// client did not say — better an approximate time than none, since the
// offline conversion upload wants one.
//
// The input is never mutated; the request struct may still be logged or
// echoed by a caller.
func normalizeSignupAttribution(input *models.SignupAttribution) *models.SignupAttribution {
	if input == nil {
		return nil
	}

	clip := func(value string) string {
		value = strings.TrimSpace(value)
		if len(value) > signupAttributionMaxLen {
			value = value[:signupAttributionMaxLen]
		}

		return value
	}

	out := &models.SignupAttribution{
		Source:      clip(input.Source),
		Medium:      clip(input.Medium),
		Campaign:    clip(input.Campaign),
		Term:        clip(input.Term),
		Content:     clip(input.Content),
		LandingPath: clip(input.LandingPath),
	}

	kind := strings.ToLower(strings.TrimSpace(input.ClickIDKind))
	if isKnownClickIDKind(kind) && strings.TrimSpace(input.ClickID) != "" {
		out.ClickIDKind = kind
		out.ClickID = clip(input.ClickID)
	}

	if out.IsEmpty() {
		return nil
	}

	if input.CapturedAt != nil && !input.CapturedAt.IsZero() {
		captured := input.CapturedAt.UTC()
		out.CapturedAt = &captured
	} else {
		now := time.Now().UTC()
		out.CapturedAt = &now
	}

	return out
}

// signupAttributionFromState reads the attribution back out of a pending
// registration entry. The state store round-trips through JSON, so the value
// arrives as a generic map (or, straight after Register in the same process,
// as the typed struct); re-encoding it is the one decoding path that handles
// both without a type switch on every field.
//
// Returns nil on any problem: a corrupt attribution must never block the
// confirmation of an otherwise valid registration.
func signupAttributionFromState(raw any) *models.SignupAttribution {
	if raw == nil {
		return nil
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}

	var attribution models.SignupAttribution
	if err := json.Unmarshal(encoded, &attribution); err != nil {
		return nil
	}

	// Normalize again on the way out: the store is trusted, but the same
	// bounds applying on both sides is what makes them bounds.
	return normalizeSignupAttribution(&attribution)
}
