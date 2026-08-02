package msteams

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// Why a linking code exists at all.
//
// Slack proves that the org clicking "install" and the workspace being
// installed into are the same actor by carrying org context through the OAuth
// redirect into the callback. Bot Framework has no OAuth redirect: the only
// thing that ever arrives from Microsoft is an activity, and an activity
// carries a tenant id but nothing that says which SolidPing organization owns
// that tenant.
//
// An Entra tenant id is a semi-public identifier, not a secret, so letting an
// org simply *assert* one in the dashboard would let any admin claim any
// tenant — and then receive that tenant's conversation references, channel
// names, and (via the shared SaaS bot credential) the ability to post into its
// channels. The linking code closes that hole: the dashboard mints a
// short-lived one-time code for a specific org, and the tenant id is only ever
// written from a real, signature-verified Bot Framework activity that quoted
// that code back. Possession of the code is the proof that ties the two sides
// together.
const (
	// linkCodeStatePrefix namespaces link codes in the shared state_entries
	// table.
	linkCodeStatePrefix = "msteams/link/"
	// LinkCodeTTL is how long a minted code stays redeemable. Long enough to
	// sideload the app package and add the bot to a team, short enough that a
	// leaked code is not a standing liability.
	LinkCodeTTL = 30 * time.Minute
	// linkCodeGroup / linkCodeGroups shape the printed form (ABCDE-FGHIJ):
	// 10 characters over a 32-symbol alphabet is ~50 bits, and the only way to
	// present a guess is a signature-verified activity relayed by Microsoft.
	linkCodeGroup  = 5
	linkCodeGroups = 2
	// linkCodeAlphabet omits I, O, 0 and 1 so a transcribed code cannot be
	// ambiguous.
	linkCodeAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
)

// Payload keys of a stored link code.
const (
	linkCodeKeyOrgUID  = "organization_uid"
	linkCodeKeyConnUID = "connection_uid"
)

var (
	// ErrInvalidLinkCode is returned when a code is unknown, expired, or
	// already redeemed. Callers must not distinguish these back to the user —
	// that would be an oracle.
	ErrInvalidLinkCode = errors.New("link code is invalid or expired")
	// ErrTenantAlreadyLinked is returned when the tenant is already bound to
	// a different organization's connection. One tenant maps to exactly one
	// organization, which is what makes inbound routing unambiguous.
	ErrTenantAlreadyLinked = errors.New("this Microsoft 365 tenant is already linked to another SolidPing organization")
)

// normalizeLinkCode makes redemption forgiving of how a human retypes a code:
// case, spaces, and the cosmetic group separator are all ignored.
func normalizeLinkCode(raw string) string {
	var out strings.Builder

	for _, char := range strings.ToUpper(strings.TrimSpace(raw)) {
		if strings.ContainsRune(linkCodeAlphabet, char) {
			out.WriteRune(char)
		}
	}

	return out.String()
}

// formatLinkCode inserts the cosmetic separator for display.
func formatLinkCode(code string) string {
	groups := make([]string, 0, linkCodeGroups)

	for start := 0; start < len(code); start += linkCodeGroup {
		end := min(start+linkCodeGroup, len(code))
		groups = append(groups, code[start:end])
	}

	return strings.Join(groups, "-")
}

// linkCodeStateKey is the state_entries key for a normalized code.
func linkCodeStateKey(code string) string {
	return linkCodeStatePrefix + code
}

// generateLinkCode mints a cryptographically random code and persists it
// against the org + connection it authorizes, with LinkCodeTTL.
func generateLinkCode(
	ctx context.Context, dbService db.Service, orgUID, connectionUID string,
) (string, error) {
	raw := make([]byte, 0, linkCodeGroup*linkCodeGroups)

	for range cap(raw) {
		idx, err := rand.Int(rand.Reader, big.NewInt(int64(len(linkCodeAlphabet))))
		if err != nil {
			return "", fmt.Errorf("generating msteams link code: %w", err)
		}

		raw = append(raw, linkCodeAlphabet[idx.Int64()])
	}

	code := string(raw)

	value := &models.JSONMap{
		linkCodeKeyOrgUID:  orgUID,
		linkCodeKeyConnUID: connectionUID,
	}

	ttl := LinkCodeTTL
	if err := dbService.SetStateEntry(ctx, nil, linkCodeStateKey(code), value, &ttl); err != nil {
		return "", fmt.Errorf("persisting msteams link code: %w", err)
	}

	return formatLinkCode(code), nil
}

// redeemLinkCode consumes a code and returns the org + connection it
// authorizes. Redemption is single-use and race-safe: the delete is the
// arbiter, so two activities quoting the same code can never both succeed.
func redeemLinkCode(ctx context.Context, dbService db.Service, raw string) (string, string, error) {
	code := normalizeLinkCode(raw)
	if code == "" {
		return "", "", ErrInvalidLinkCode
	}

	key := linkCodeStateKey(code)

	entry, err := dbService.GetStateEntry(ctx, nil, key)
	if err != nil {
		return "", "", fmt.Errorf("looking up msteams link code: %w", err)
	}

	if entry == nil || entry.Value == nil {
		return "", "", ErrInvalidLinkCode
	}

	// Delete first and let the result decide: only the caller that actually
	// removed the live entry may proceed.
	deleted, err := dbService.DeleteStateEntry(ctx, nil, key)
	if err != nil {
		return "", "", fmt.Errorf("consuming msteams link code: %w", err)
	}

	if !deleted {
		return "", "", ErrInvalidLinkCode
	}

	orgUID, _ := (*entry.Value)[linkCodeKeyOrgUID].(string)
	connUID, _ := (*entry.Value)[linkCodeKeyConnUID].(string)

	if orgUID == "" || connUID == "" {
		return "", "", ErrInvalidLinkCode
	}

	return orgUID, connUID, nil
}
