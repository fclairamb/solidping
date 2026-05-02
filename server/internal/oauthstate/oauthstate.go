// Package oauthstate is a tiny CSRF-state primitive shared by every OAuth
// flow in the codebase. Each entry is keyed on `<kind>:<nonce>` so a state
// minted for one flow (e.g., sign-in) cannot be redeemed by a callback that
// expected another (e.g., install). Validate is single-use — it deletes the
// entry as part of the validation, so a captured nonce cannot be replayed.
package oauthstate

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// ErrInvalidState is returned when the state is missing, expired, already
// used, or was minted for a different kind. Callers must not differentiate
// between the failure modes back to the user — that would be a side channel.
var ErrInvalidState = errors.New("oauth state invalid or expired")

// keyState matches the JSONMap key used by the legacy Slack/GitHub/GitLab
// sign-in flows so existing rows in `state_entries` remain readable.
const keyState = "state"

// nonceLen is the unencoded byte length. base64url-encoded that becomes a
// 43-char string (no padding).
const nonceLen = 32

// Entry is the persisted shape. Payload is opaque to this package — the
// caller decides what to stash there (redirect URI for sign-in, an exchange
// token bundle for the post-callback handoff, etc.).
type Entry struct {
	Nonce     string         `json:"nonce"`
	Kind      string         `json:"kind"`
	CreatedAt int64          `json:"createdAt"`
	Payload   map[string]any `json:"payload,omitempty"`
}

// Generate mints a fresh nonce, persists an Entry under "<kind>:<nonce>" with
// the requested TTL, and returns the nonce so the caller can pass it to the
// remote OAuth provider as the `state` parameter.
func Generate(
	ctx context.Context, dbService db.Service, kind string, payload map[string]any, ttl time.Duration,
) (string, error) {
	if kind == "" {
		return "", fmt.Errorf("oauthstate: kind is required")
	}

	nonceBytes := make([]byte, nonceLen)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", fmt.Errorf("oauthstate: generate nonce: %w", err)
	}

	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)

	entry := Entry{
		Nonce:     nonce,
		Kind:      kind,
		CreatedAt: time.Now().Unix(),
		Payload:   payload,
	}

	encoded, err := json.Marshal(entry)
	if err != nil {
		return "", fmt.Errorf("oauthstate: marshal entry: %w", err)
	}

	value := &models.JSONMap{keyState: string(encoded)}

	storageKey := storageKey(kind, nonce)
	if err := dbService.SetStateEntry(ctx, nil, storageKey, value, &ttl); err != nil {
		return "", fmt.Errorf("oauthstate: persist entry: %w", err)
	}

	return nonce, nil
}

// Validate fetches the Entry, deletes it (single-use), and returns it on
// success. Returns ErrInvalidState if the entry is missing, expired, or was
// minted for a different kind. The caller should not log the underlying
// reason at info level — return a generic error to the user.
func Validate(
	ctx context.Context, dbService db.Service, kind, nonce string,
) (*Entry, error) {
	if kind == "" || nonce == "" {
		return nil, ErrInvalidState
	}

	storageKey := storageKey(kind, nonce)

	stateEntry, err := dbService.GetStateEntry(ctx, nil, storageKey)
	if err != nil || stateEntry == nil {
		return nil, ErrInvalidState
	}

	// Single-use: delete eagerly so a concurrent re-submit fails. We ignore
	// the delete error — even if it fails the entry's TTL will eventually
	// expire it; the worst case is a brief replay window for a state we
	// already validated.
	_ = dbService.DeleteStateEntry(ctx, nil, storageKey)

	if stateEntry.Value == nil {
		return nil, ErrInvalidState
	}

	raw, ok := (*stateEntry.Value)[keyState].(string)
	if !ok {
		return nil, ErrInvalidState
	}

	var entry Entry
	if err := json.Unmarshal([]byte(raw), &entry); err != nil {
		return nil, ErrInvalidState
	}

	if entry.Kind != kind || entry.Nonce != nonce {
		return nil, ErrInvalidState
	}

	// ExpiresAt is enforced by the storage layer when set, but we also check
	// the embedded CreatedAt against the original TTL via the storage
	// expiry. If the row is still here, the storage hasn't reaped it yet —
	// trust the caller-supplied TTL semantics.
	if stateEntry.ExpiresAt != nil && time.Now().After(*stateEntry.ExpiresAt) {
		return nil, ErrInvalidState
	}

	return &entry, nil
}

func storageKey(kind, nonce string) string {
	return "oauth_state:" + kind + ":" + nonce
}
