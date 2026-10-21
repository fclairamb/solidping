package auth

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// keyPreferences is the pending-registration state key that carries the
// signup form's language and timezone to ConfirmRegistration.
const keyPreferences = "preferences"

// Preference validation errors (PATCH /auth/me answers 400 VALIDATION_ERROR).
var (
	ErrUnsupportedLocale = errors.New("locale must be one of en, fr, de, es")
	ErrInvalidTimezone   = errors.New("timezone must be an IANA zone name, e.g. Europe/Paris")
)

// PreferencesRequest is the partial preferences update of PATCH /auth/me.
// A nil field is left alone; an empty string clears it.
type PreferencesRequest struct {
	Locale   *string `json:"locale,omitempty"`
	Timezone *string `json:"timezone,omitempty"`
}

// mergePreferences applies a partial update to the stored preferences. A
// locale is stored normalized ("fr-FR" becomes "fr"), a timezone as its
// canonical IANA name. The input is never mutated.
func mergePreferences(current *models.UserPreferences, req *PreferencesRequest) (*models.UserPreferences, error) {
	merged := models.UserPreferences{}
	if current != nil {
		merged = *current
	}

	if req.Locale != nil {
		merged.Locale = ""

		if strings.TrimSpace(*req.Locale) != "" {
			merged.Locale = i18n.Normalize(*req.Locale)
			if merged.Locale == "" {
				return nil, ErrUnsupportedLocale
			}
		}
	}

	if req.Timezone != nil {
		merged.Timezone = ""

		if strings.TrimSpace(*req.Timezone) != "" {
			loc, err := i18n.ParseTimezone(*req.Timezone)
			if err != nil {
				return nil, ErrInvalidTimezone
			}

			merged.Timezone = loc.String()
		}
	}

	return &merged, nil
}

// signupPreferences turns what a register or accept-invite form sent into the
// preferences a new account starts with. Lenient on purpose: a browser
// language the server does not support, or an odd timezone, is dropped rather
// than failing the signup. nil when nothing usable remains.
func signupPreferences(locale, timezone string) *models.UserPreferences {
	prefs := &models.UserPreferences{Locale: i18n.Normalize(locale)}

	if loc, err := i18n.ParseTimezone(timezone); err == nil {
		prefs.Timezone = loc.String()
	}

	if prefs.IsEmpty() {
		return nil
	}

	return prefs
}

// signupPreferencesFromState reads the preferences back out of a pending
// registration entry (a generic map after the state store's JSON round trip)
// and validates them again. nil on any problem: a corrupt value must never
// block a confirmation.
func signupPreferencesFromState(raw any) *models.UserPreferences {
	if raw == nil {
		return nil
	}

	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}

	var prefs models.UserPreferences
	if err := json.Unmarshal(encoded, &prefs); err != nil {
		return nil
	}

	return signupPreferences(prefs.Locale, prefs.Timezone)
}

// preferencesInfo is the preferences object /auth/me and every login response
// carry: never nil, so a client can tell "nothing set" ({}) from an old server.
func preferencesInfo(user *models.User) *models.UserPreferences {
	if user.Preferences == nil {
		return &models.UserPreferences{}
	}

	prefs := *user.Preferences

	return &prefs
}
