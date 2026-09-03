package opsnotify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fclairamb/solidping/server/internal/db"
)

// ParamOperatorNotifications is the system-parameter key operator
// notifications are configured through. It sits next to
// `watchdog.ParamPlatformWatchdog` and, like it, is editable live by a super
// admin — adding an operator must never need a redeploy.
const ParamOperatorNotifications = "operator_notifications"

// Recipient is one subscribed user.
type Recipient struct {
	// UserUID is the subscribed user. They must be a super admin AT DELIVERY
	// TIME, not merely when the row was written — see ResolveRecipients.
	UserUID string `json:"userUid"`
	// Events is what they asked to hear about. Every entry must be in
	// SubscribableEvents(); an empty list means the recipient is subscribed to
	// nothing and is rejected at write time, because "saved but silent" is the
	// exact misconfiguration this feature exists to remove.
	Events []string `json:"events"`
}

// Wants reports whether this recipient subscribed to an event.
func (r Recipient) Wants(event string) bool {
	for _, candidate := range r.Events {
		if candidate == event {
			return true
		}
	}

	return false
}

// Config is the decoded `operator_notifications` system parameter.
type Config struct {
	// Enabled gates the whole feature. False means no notice is ever
	// delivered; the events still happen and are still logged by their own
	// subsystems.
	Enabled bool `json:"enabled"`
	// Recipients is the subscription table.
	Recipients []Recipient `json:"recipients"`
}

// DefaultConfig is the feature OFF with nobody subscribed — what every
// instance that never wrote the parameter gets.
func DefaultConfig() *Config {
	return &Config{Enabled: false, Recipients: nil}
}

// RecipientsFor returns the user UIDs subscribed to an event, in
// configuration order. It does NOT check super-admin status; that is
// ResolveRecipients' job, and it is deliberately a separate step so the
// authorization check happens against live data at delivery time.
func (c *Config) RecipientsFor(event string) []string {
	if c == nil || !c.Enabled {
		return nil
	}

	out := make([]string, 0, len(c.Recipients))

	for _, recipient := range c.Recipients {
		if recipient.Wants(event) {
			out = append(out, recipient.UserUID)
		}
	}

	return out
}

// Parameter validation errors.
var (
	// ErrInvalidParameterShape is returned when the value is not a JSON object.
	ErrInvalidParameterShape = errors.New("operator_notifications must be a JSON object")
	// ErrInvalidRecipient is returned for a blank userUid.
	ErrInvalidRecipient = errors.New("operator_notifications recipients must carry a non-empty userUid")
	// ErrDuplicateRecipient is returned when the same user appears twice. Two
	// rows for one human is not a merge instruction, it is a mistake that
	// would double every notice they receive.
	ErrDuplicateRecipient = errors.New("operator_notifications lists the same recipient twice")
	// ErrNoEvents is returned for a recipient subscribed to nothing.
	ErrNoEvents = errors.New("operator_notifications recipients must subscribe to at least one event")
	// ErrInvalidEvent is returned for an event name outside SubscribableEvents.
	ErrInvalidEvent = errors.New("operator_notifications carries an unknown event")
	// ErrNotSuperAdmin is returned when a recipient is not a super admin.
	// Support-thread content is RequireSuperAdmin-gated, so a notice quoting a
	// customer's message must not be routable to someone who could not open
	// the thread it came from.
	ErrNotSuperAdmin = errors.New("operator_notifications recipients must be super admins")
)

// ValidateParameter rejects an `operator_notifications` value the transport
// could not act on.
//
// This runs on the WRITE, not on the read. A typo in an event name would
// otherwise silently subscribe an operator to nothing at all, and they would
// only find out by not being paged — which is the failure this feature exists
// to remove, reintroduced one layer up.
//
// The super-admin check needs the database, so it lives in
// ValidateParameterWithDB; this function is the shape-only half and stays
// usable from a pure unit test.
func ValidateParameter(value any) error {
	cfg, err := decodeConfig(value)
	if err != nil {
		return err
	}

	seen := make(map[string]bool, len(cfg.Recipients))

	for _, recipient := range cfg.Recipients {
		uid := strings.TrimSpace(recipient.UserUID)
		if uid == "" {
			return ErrInvalidRecipient
		}

		if seen[uid] {
			return fmt.Errorf("%w (%s)", ErrDuplicateRecipient, uid)
		}

		seen[uid] = true

		if len(recipient.Events) == 0 {
			return fmt.Errorf("%w (%s)", ErrNoEvents, uid)
		}

		for _, event := range recipient.Events {
			if !IsSubscribableEvent(event) {
				return fmt.Errorf("%w %q; valid events are %s",
					ErrInvalidEvent, event, strings.Join(SubscribableEvents(), ", "))
			}
		}
	}

	return nil
}

// ValidateParameterWithDB is ValidateParameter plus the super-admin check on
// every named recipient.
//
// Write-time rejection is a usability guarantee, not a security one — the
// delivery path re-checks against live data (ResolveRecipients), because a
// user can lose super_admin long after the parameter was saved.
func ValidateParameterWithDB(ctx context.Context, dbSvc db.Service, value any) error {
	if err := ValidateParameter(value); err != nil {
		return err
	}

	if dbSvc == nil {
		return nil
	}

	cfg, err := decodeConfig(value)
	if err != nil {
		return err
	}

	for _, recipient := range cfg.Recipients {
		uid := strings.TrimSpace(recipient.UserUID)

		user, getErr := dbSvc.GetUser(ctx, uid)
		if getErr != nil {
			return fmt.Errorf("looking up operator_notifications recipient %s: %w", uid, getErr)
		}

		if user == nil {
			return fmt.Errorf("%w (%s is not a user)", ErrInvalidRecipient, uid)
		}

		if !user.SuperAdmin {
			return fmt.Errorf("%w (%s)", ErrNotSuperAdmin, user.Email)
		}
	}

	return nil
}

// decodeConfig round-trips an arbitrary parameter value into a Config.
func decodeConfig(value any) (*Config, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidParameterShape, err)
	}

	cfg := &Config{}
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidParameterShape, err)
	}

	return cfg, nil
}

// LoadConfig reads and decodes the `operator_notifications` system parameter.
// A missing parameter yields the disabled default, which is the correct
// behavior for every instance that has never opted in.
func LoadConfig(ctx context.Context, dbSvc db.Service) (*Config, error) {
	param, err := dbSvc.GetSystemParameter(ctx, ParamOperatorNotifications)
	if err != nil {
		return nil, fmt.Errorf("get operator_notifications parameter: %w", err)
	}

	if param == nil || param.Value == nil {
		return DefaultConfig(), nil
	}

	cfg, err := decodeConfig(param.Value["value"])
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// ResolveRecipients turns the configured subscription list for one event into
// the user UIDs that may actually be delivered to.
//
// THE SUPER-ADMIN CHECK HAPPENS HERE, at delivery time, on live data — not
// only at write time. Support-thread content is RequireSuperAdmin-gated
// (handlers/supportinbox/routes.go), so a notice that quotes a customer's
// message must not reach a user who can no longer open the thread. A recipient
// who has since lost the flag is skipped with a WARN naming them.
func ResolveRecipients(
	ctx context.Context, dbSvc db.Service, log *slog.Logger, cfg *Config, event string,
) []string {
	candidates := cfg.RecipientsFor(event)
	out := make([]string, 0, len(candidates))

	for _, uid := range candidates {
		user, err := dbSvc.GetUser(ctx, uid)
		if err != nil || user == nil {
			log.WarnContext(ctx, "Operator notice recipient no longer exists; skipping",
				"recipientUid", uid, "event", event, "error", err)

			continue
		}

		if !user.SuperAdmin {
			log.WarnContext(ctx,
				"Operator notice recipient is no longer a super admin; skipping "+
					"(the notice may quote support content only super admins may read)",
				"recipientUid", uid, "email", user.Email, "event", event)

			continue
		}

		out = append(out, uid)
	}

	return out
}
