// Package sealing holds the org-level state of credential sealing for private
// regions (spec 2026-11-08-01): the browser-sealing mode of an org, the shared
// region keys, and the recipient list a sealed blob for a set of private
// regions must be sealed to. Both the server-side sealing path and the
// browser-sealing write API read the recipients from here, so they can never
// disagree on who a blob is for.
package sealing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regions"
)

// Org parameter keys. Both are org-scoped `parameters` rows: no migration.
const (
	// ParamKeyBrowserSealingMode holds ModeOptional or ModeRequired.
	ParamKeyBrowserSealingMode = "security.browser_sealing.mode"
	// ParamKeyRegionKeys holds a JSON map region -> RegionKey.
	ParamKeyRegionKeys = "sealing.region_keys"
	// ParamKeyRetiredRegionKeys holds a JSON map fingerprint -> RegionKey of
	// the region keys rotated away or removed (spec 2026-11-11-02): blobs
	// still sealed to one can only be handed to an agent that proves it holds
	// it, which takes its public key.
	ParamKeyRetiredRegionKeys = "sealing.retired_region_keys"
)

// maxRetiredRegionKeys bounds the retired key list: the oldest go first.
const maxRetiredRegionKeys = 64

// Browser-sealing modes of an org.
const (
	// ModeOptional (default): server-side and browser encryption, per check.
	ModeOptional = "optional"
	// ModeRequired: plaintext secret fields are refused on checks.
	ModeRequired = "required"
)

// Region key modes of a private region.
const (
	KeyModePerAgent = "perAgent"
	KeyModeShared   = "shared"
)

// Recipient kinds.
const (
	RecipientKindRegion = "region"
	RecipientKindAgent  = "agent"
)

// regionKeyCapabilityPrefix prefixes the capability an agent advertises for
// each region key it holds. The capability set only accepts [a-z0-9-] names,
// so the spec's `region-key:<fp>` is spelled `region-key-<fp>`.
const regionKeyCapabilityPrefix = "region-key-"

// ErrInvalidMode is returned for a browser-sealing mode other than the two.
var ErrInvalidMode = errors.New("browser sealing mode must be optional or required")

// Store is the subset of db.Service this package reads and writes.
type Store interface {
	GetOrgParameter(ctx context.Context, orgUID, key string) (*models.Parameter, error)
	SetOrgParameter(ctx context.Context, orgUID, key string, value any, secret bool) error
	DeleteOrgParameter(ctx context.Context, orgUID, key string) error
	ListActiveAgentsByRegion(ctx context.Context, orgUID, region string) ([]*models.Agent, error)
}

// RegionKey is the shared X25519 key of one private region. Only the public
// recipient is stored; the identity lives on the region's agents.
type RegionKey struct {
	Recipient   string    `json:"recipient"`
	Fingerprint string    `json:"fingerprint"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Recipient is one key a blob for a set of private regions is sealed to.
type Recipient struct {
	Kind        string `json:"kind"`
	Region      string `json:"region"`
	AgentUID    string `json:"agentUid,omitempty"`
	AgentName   string `json:"agentName,omitempty"`
	Recipient   string `json:"recipient"`
	Fingerprint string `json:"fingerprint"`
}

// BrowserSealingMode returns the org's mode, ModeOptional when unset.
func BrowserSealingMode(ctx context.Context, store Store, orgUID string) (string, error) {
	param, err := store.GetOrgParameter(ctx, orgUID, ParamKeyBrowserSealingMode)
	if err != nil {
		return "", fmt.Errorf("read browser sealing mode: %w", err)
	}

	if param != nil {
		if value, ok := param.Value[models.ParameterValueKey].(string); ok && value == ModeRequired {
			return ModeRequired, nil
		}
	}

	return ModeOptional, nil
}

// SetBrowserSealingMode stores the org's mode. ModeOptional deletes the row.
func SetBrowserSealingMode(ctx context.Context, store Store, orgUID, mode string) error {
	switch mode {
	case ModeOptional:
		return store.DeleteOrgParameter(ctx, orgUID, ParamKeyBrowserSealingMode)
	case ModeRequired:
		return store.SetOrgParameter(ctx, orgUID, ParamKeyBrowserSealingMode, ModeRequired, false)
	default:
		return ErrInvalidMode
	}
}

// RegionKeys returns the org's shared region keys, keyed by private region.
// A missing or malformed row reads as none (every region per agent). An entry
// whose region no longer exists (a key left behind by a deleted region) is
// not returned, so a new region with the same slug never inherits it.
func RegionKeys(ctx context.Context, store Store, orgUID string) (map[string]RegionKey, error) {
	out, err := StoredRegionKeys(ctx, store, orgUID)
	if err != nil || len(out) == 0 {
		return out, err
	}

	existing, err := existingPrivateRegions(ctx, store, orgUID)
	if err != nil {
		return nil, err
	}

	for region := range out {
		if _, ok := existing[region]; !ok {
			delete(out, region)
		}
	}

	return out, nil
}

// existingPrivateRegions returns the canonical `@slug` of the org's private regions.
func existingPrivateRegions(ctx context.Context, store Store, orgUID string) (map[string]struct{}, error) {
	param, err := store.GetOrgParameter(ctx, orgUID, regions.ParamCustomRegions)
	if err != nil {
		return nil, fmt.Errorf("read custom regions: %w", err)
	}

	out := map[string]struct{}{}

	if param == nil {
		return out, nil
	}

	raw, err := json.Marshal(param.Value[models.ParameterValueKey])
	if err != nil {
		return out, nil //nolint:nilerr // a malformed row reads as no region
	}

	var defs []regions.RegionDefinition
	if err := json.Unmarshal(raw, &defs); err != nil {
		return out, nil //nolint:nilerr // same
	}

	for i := range defs {
		out[regions.PrivateRegionSlug(defs[i].Slug)] = struct{}{}
	}

	return out, nil
}

// StoredRegionKeys returns the stored region keys as they are, including the
// ones of regions that no longer exist.
func StoredRegionKeys(ctx context.Context, store Store, orgUID string) (map[string]RegionKey, error) {
	param, err := store.GetOrgParameter(ctx, orgUID, ParamKeyRegionKeys)
	if err != nil {
		return nil, fmt.Errorf("read region keys: %w", err)
	}

	out := map[string]RegionKey{}

	if param == nil {
		return out, nil
	}

	raw, err := json.Marshal(param.Value[models.ParameterValueKey])
	if err != nil {
		return out, nil //nolint:nilerr // a malformed row reads as no key
	}

	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]RegionKey{}, nil //nolint:nilerr // same
	}

	for region := range out {
		if recipient := out[region].Recipient; recipient == "" || credentials.ValidateRecipient(recipient) != nil {
			delete(out, region)
		}
	}

	return out, nil
}

// SetRegionKeys stores the org's shared region keys. An empty map deletes the
// row.
func SetRegionKeys(ctx context.Context, store Store, orgUID string, keys map[string]RegionKey) error {
	if len(keys) == 0 {
		return store.DeleteOrgParameter(ctx, orgUID, ParamKeyRegionKeys)
	}

	return store.SetOrgParameter(ctx, orgUID, ParamKeyRegionKeys, keys, false)
}

// RetiredRegionKeys returns the org's retired region keys, keyed by
// fingerprint. A missing or malformed row reads as none.
func RetiredRegionKeys(ctx context.Context, store Store, orgUID string) (map[string]RegionKey, error) {
	param, err := store.GetOrgParameter(ctx, orgUID, ParamKeyRetiredRegionKeys)
	if err != nil {
		return nil, fmt.Errorf("read retired region keys: %w", err)
	}

	out := map[string]RegionKey{}

	if param == nil {
		return out, nil
	}

	raw, err := json.Marshal(param.Value[models.ParameterValueKey])
	if err != nil {
		return out, nil //nolint:nilerr // a malformed row reads as no key
	}

	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]RegionKey{}, nil //nolint:nilerr // same
	}

	for fingerprint := range out {
		if recipient := out[fingerprint].Recipient; recipient == "" ||
			credentials.RecipientFingerprint(recipient) != fingerprint {
			delete(out, fingerprint)
		}
	}

	return out, nil
}

// RetireRegionKey remembers a region key that was rotated away or removed.
// The list keeps the maxRetiredRegionKeys most recent ones.
func RetireRegionKey(ctx context.Context, store Store, orgUID string, key RegionKey, now time.Time) error {
	retired, err := RetiredRegionKeys(ctx, store, orgUID)
	if err != nil {
		return err
	}

	key.CreatedAt = now.UTC()
	retired[key.Fingerprint] = key

	for len(retired) > maxRetiredRegionKeys {
		oldest := ""

		for fingerprint := range retired {
			if oldest == "" || retired[fingerprint].CreatedAt.Before(retired[oldest].CreatedAt) {
				oldest = fingerprint
			}
		}

		delete(retired, oldest)
	}

	return store.SetOrgParameter(ctx, orgUID, ParamKeyRetiredRegionKeys, retired, false)
}

// KnownRegionRecipients maps the fingerprint of every region key of the org,
// current or retired, to its public key. It reads the stored keys, including
// those of regions that no longer exist: a key is never reusable as an agent key.
func KnownRegionRecipients(ctx context.Context, store Store, orgUID string) (map[string]string, error) {
	current, err := StoredRegionKeys(ctx, store, orgUID)
	if err != nil {
		return nil, err
	}

	retired, err := RetiredRegionKeys(ctx, store, orgUID)
	if err != nil {
		return nil, err
	}

	currentLen := len(current)
	retiredLen := len(retired)
	if currentLen > math.MaxInt-retiredLen {
		return nil, fmt.Errorf("known region recipients too large")
	}

	out := make(map[string]string, currentLen+retiredLen)
	for fingerprint := range retired {
		out[fingerprint] = retired[fingerprint].Recipient
	}

	for region := range current {
		recipient := current[region].Recipient
		out[credentials.RecipientFingerprint(recipient)] = recipient
	}

	return out, nil
}

// NewRegionKey builds the stored record of a region recipient.
func NewRegionKey(recipient string, now time.Time) (RegionKey, error) {
	if err := credentials.ValidateRecipient(recipient); err != nil {
		return RegionKey{}, err
	}

	return RegionKey{
		Recipient:   recipient,
		Fingerprint: credentials.RecipientFingerprint(recipient),
		CreatedAt:   now.UTC(),
	}, nil
}

// RecipientsForRegions returns what a blob for these private regions must be
// sealed to: the region key of a shared region, the active agents of a
// per-agent one. Duplicate recipients are listed once.
func RecipientsForRegions(
	ctx context.Context, store Store, orgUID string, privateRegions []string,
) ([]Recipient, error) {
	keys, err := RegionKeys(ctx, store, orgUID)
	if err != nil {
		return nil, err
	}

	out := make([]Recipient, 0, len(privateRegions))
	seen := make(map[string]struct{})

	for _, region := range privateRegions {
		if key, shared := keys[region]; shared {
			if _, dup := seen[key.Recipient]; !dup {
				seen[key.Recipient] = struct{}{}
				out = append(out, Recipient{
					Kind: RecipientKindRegion, Region: region,
					Recipient: key.Recipient, Fingerprint: key.Fingerprint,
				})
			}

			continue
		}

		agents, listErr := store.ListActiveAgentsByRegion(ctx, orgUID, region)
		if listErr != nil {
			return nil, fmt.Errorf("list active agents for %s: %w", region, listErr)
		}

		for _, agent := range agents {
			if agent.X25519PublicKey == "" {
				continue
			}

			if _, dup := seen[agent.X25519PublicKey]; dup {
				continue
			}

			seen[agent.X25519PublicKey] = struct{}{}
			out = append(out, Recipient{
				Kind: RecipientKindAgent, Region: region,
				AgentUID: agent.UID, AgentName: agent.Name,
				Recipient: agent.X25519PublicKey, Fingerprint: credentials.RecipientFingerprint(agent.X25519PublicKey),
			})
		}
	}

	return out, nil
}

// RecipientKeys returns the age recipient strings of a recipient list.
func RecipientKeys(recipients []Recipient) []string {
	out := make([]string, 0, len(recipients))
	for i := range recipients {
		out = append(out, recipients[i].Recipient)
	}

	return out
}

// SortedFingerprints returns the sorted fingerprints of a recipient list.
func SortedFingerprints(recipients []Recipient) []string {
	out := make([]string, 0, len(recipients))
	for i := range recipients {
		out = append(out, recipients[i].Fingerprint)
	}

	sort.Strings(out)

	return out
}

// RegionKeyCapability is the capability an agent advertises for a region key
// it holds. It never carries the key itself.
func RegionKeyCapability(fingerprint string) string {
	return regionKeyCapabilityPrefix + fingerprint
}

// RegionKeyFingerprints picks the region key fingerprints out of a capability
// report.
func RegionKeyFingerprints(capabilities []string) []string {
	var out []string

	for _, name := range capabilities {
		if fp, ok := strings.CutPrefix(name, regionKeyCapabilityPrefix); ok && fp != "" {
			out = append(out, fp)
		}
	}

	return out
}
