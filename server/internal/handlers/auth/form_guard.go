package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/fclairamb/solidping/server/internal/prommetrics"
)

// The public forms the form guard protects. Each one makes the server email an
// address an anonymous visitor typed, which is what a "subscription bombing"
// bot abuses: it signs its victims up everywhere so their mailbox drowns.
const (
	FormRegister      = "register"
	FormPasswordReset = "password-reset"
)

const (
	// formTicketPurpose derives the ticket's signing key from the JWT secret
	// (purposeKey), so a ticket can never verify as a session token or as any
	// other purpose's token.
	formTicketPurpose = "form-ticket"
	// formTicketNonceSize is the random ticket ID that makes it single-use.
	formTicketNonceSize = 16

	formTicketUsedKeyPrefix = "form_ticket_used:"
	formIPKeyPrefix         = "form_rl_ip:"
	formEmailKeyPrefix      = "form_rl_email:"
	formBudgetWindow        = time.Hour
)

// Reasons the form guard drops a submission. They label
// solidping_auth_form_dropped_total and the INFO log line.
const (
	formDropHoneypot         = "honeypot"
	formDropMissingTicket    = "missing_ticket"
	formDropInvalidTicket    = "invalid_ticket"
	formDropExpiredTicket    = "expired_ticket"
	formDropWrongForm        = "wrong_form"
	formDropTooFast          = "too_fast"
	formDropReplayedTicket   = "replayed_ticket"
	formDropIPRateLimited    = "ip_rate_limited"
	formDropEmailRateLimited = "email_rate_limited"
)

// ErrUnknownForm is returned when a form ticket is requested for a form the
// guard does not know.
var ErrUnknownForm = errors.New("unknown form")

// FormGuardFields are the anti-bot fields the sign-up and password-reset
// requests carry. The dashboard fills FormTicket from GET
// /api/v1/auth/form-ticket when the form renders, and leaves Website empty:
// it is a honeypot input a human never sees.
type FormGuardFields struct {
	FormTicket string `json:"formTicket,omitempty"`
	Website    string `json:"website,omitempty"`
}

// FormTicketResponse is the body of GET /api/v1/auth/form-ticket.
type FormTicketResponse struct {
	Ticket string `json:"ticket"`
	// MinAgeMs is how old the ticket must be before the form is submitted.
	// A non-browser client (the sp CLI) waits for it; the dashboard does not
	// need to, a human is slower.
	MinAgeMs  int64     `json:"minAgeMs"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// formTicketClaims is the signed payload. IssuedAtMs carries the issue time in
// milliseconds because the registered iat claim only has second precision.
type formTicketClaims struct {
	Form       string `json:"form"`
	IssuedAtMs int64  `json:"iatMs"`
	jwt.RegisteredClaims
}

func knownForm(form string) bool {
	return form == FormRegister || form == FormPasswordReset
}

// IssueFormTicket mints a signed ticket for form, stamped with the current
// time. Tickets are issued whether the guard is enabled or not, so the
// dashboard does not depend on the server's configuration.
func (s *Service) IssueFormTicket(form string) (*FormTicketResponse, error) {
	return s.issueFormTicketAt(form, time.Now())
}

func (s *Service) issueFormTicketAt(form string, issuedAt time.Time) (*FormTicketResponse, error) {
	if !knownForm(form) {
		return nil, ErrUnknownForm
	}

	nonce := make([]byte, formTicketNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("failed to generate form ticket nonce: %w", err)
	}

	guard := s.fullCfg.Auth.FormGuard
	expiresAt := issuedAt.Add(guard.ResolvedTicketTTL())

	signed, err := s.signJWT(formTicketPurpose, formTicketClaims{
		Form:       form,
		IssuedAtMs: issuedAt.UnixMilli(),
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        hex.EncodeToString(nonce),
			IssuedAt:  jwt.NewNumericDate(issuedAt),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to sign form ticket: %w", err)
	}

	return &FormTicketResponse{
		Ticket:    signed,
		MinAgeMs:  guard.MinFillTime.Milliseconds(),
		ExpiresAt: expiresAt.UTC(),
	}, nil
}

// screenFormSubmission runs the form guard on one submission of form, in this
// order:
//
//  1. the per-IP budget: over it, ErrRateLimited (a 429 a human behind a busy
//     NAT can read; it says nothing about the address typed). For sign-up it
//     counts only the registrations still waiting for confirmation, so a
//     team signing up from one office is not blocked once people confirm;
//  2. when the guard is enabled, the honeypot and the form ticket (present,
//     genuine, for this form, unexpired, old enough, never used before);
//  3. the per-email budget.
//
// A non-empty reason means "answer like a success and send nothing": the
// caller must not tell the client anything else. The drop is logged at INFO
// and counted in solidping_auth_form_dropped_total. A storage error on a
// budget or on the single-use record fails open (logged), like the
// password-reset counters always have.
func (s *Service) screenFormSubmission(
	ctx context.Context, form string, fields FormGuardFields, clientIP, email string,
) (string, error) {
	guard := s.fullCfg.Auth.FormGuard

	if clientIP != "" && guard.PerIPPerHour > 0 {
		limited, err := s.overFormIPBudget(ctx, form, clientIP, email, guard.PerIPPerHour)
		if err != nil {
			slog.WarnContext(ctx, "Failed to track form IP budget", "form", form, "error", err)
		} else if limited {
			s.recordFormDrop(ctx, form, formDropIPRateLimited, clientIP)

			return formDropIPRateLimited, ErrRateLimited
		}
	}

	if guard.Enabled {
		if reason := s.checkFormTicket(ctx, form, fields, time.Now()); reason != "" {
			s.recordFormDrop(ctx, form, reason, clientIP)

			return reason, nil
		}
	}

	if guard.PerEmailPerHour > 0 {
		sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(email))))
		key := formEmailKeyPrefix + form + ":" + hex.EncodeToString(sum[:])

		limited, err := s.bumpCounter(ctx, key, guard.PerEmailPerHour, formBudgetWindow)
		if err != nil {
			slog.WarnContext(ctx, "Failed to track form email budget", "form", form, "error", err)
		} else if limited {
			s.recordFormDrop(ctx, form, formDropEmailRateLimited, clientIP)

			return formDropEmailRateLimited, nil
		}
	}

	return "", nil
}

// overFormIPBudget reports whether clientIP has used up its hourly budget for
// form. A password reset only ever emails an existing account, so every
// request counts. A sign-up counts only while it is unconfirmed: the pending
// registrations created from clientIP in the last hour, which confirmation
// deletes. A bot's victims never confirm, a real team does. The address being
// signed up is left out: signing it up again replaces its own entry.
func (s *Service) overFormIPBudget(ctx context.Context, form, clientIP, email string, limit int) (bool, error) {
	if form != FormRegister {
		return s.bumpCounter(ctx, formIPKeyPrefix+form+":"+clientIP, limit, formBudgetWindow)
	}

	entries, err := s.db.ListStateEntries(ctx, nil, registrationKeyPrefix)
	if err != nil {
		return false, err
	}

	since := time.Now().Add(-formBudgetWindow)
	pending := 0

	for _, entry := range entries {
		if entry.Value == nil || entry.CreatedAt.Before(since) || entry.Key == registrationKeyPrefix+email {
			continue
		}

		if ip, _ := (*entry.Value)[keyClientIP].(string); ip == clientIP {
			pending++
		}
	}

	return pending >= limit, nil
}

// checkFormTicket returns the reason to drop the submission, or "" when the
// honeypot is empty and the ticket is valid. A valid ticket is consumed: a bot
// that waited out one ticket cannot replay it.
func (s *Service) checkFormTicket(ctx context.Context, form string, fields FormGuardFields, now time.Time) string {
	if fields.Website != "" {
		return formDropHoneypot
	}

	if fields.FormTicket == "" {
		return formDropMissingTicket
	}

	parsed, err := s.parsePurposeJWT(fields.FormTicket, formTicketPurpose, &formTicketClaims{})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return formDropExpiredTicket
		}

		return formDropInvalidTicket
	}

	claims, ok := parsed.Claims.(*formTicketClaims)
	if !ok || !parsed.Valid || claims.ID == "" || claims.ExpiresAt == nil {
		return formDropInvalidTicket
	}

	if claims.Form != form {
		return formDropWrongForm
	}

	if now.Sub(time.UnixMilli(claims.IssuedAtMs)) < s.fullCfg.Auth.FormGuard.MinFillTime {
		return formDropTooFast
	}

	// The record lives as long as the ticket could still verify, so a replay
	// is caught for the ticket's whole life and the table cleans itself.
	ttl := time.Until(claims.ExpiresAt.Time) + time.Minute

	// A budget of one, rather than SetStateEntryIfNotExists: global entries
	// have a NULL organization_uid, which the (organization_uid, key) unique
	// index never treats as a conflict, so only BumpGlobalCounter serializes
	// two concurrent uses of the same ticket.
	used, err := s.bumpCounter(ctx, formTicketUsedKeyPrefix+claims.ID, 1, ttl)
	if err != nil {
		slog.WarnContext(ctx, "Failed to record form ticket use", "form", form, "error", err)

		return ""
	}

	if used {
		return formDropReplayedTicket
	}

	return ""
}

// recordFormDrop logs and counts one dropped submission. The email address is
// deliberately left out of the log: it is usually the victim's, not the bot's.
func (s *Service) recordFormDrop(ctx context.Context, form, reason, clientIP string) {
	slog.InfoContext(ctx, "Form submission dropped by the form guard",
		"form", form, "reason", reason, "ip", clientIP)
	prommetrics.AuthFormDropped.WithLabelValues(form, reason).Inc()
}
