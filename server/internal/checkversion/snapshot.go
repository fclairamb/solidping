// Package checkversion builds the secret-free snapshot of a check definition
// that the check_versions table stores (spec 2026-10-03-06).
//
// A snapshot holds the definition only: name, slug, description, type, public
// config, group, placement, quorum, enabled, period and labels. It never holds
// config_private, config_private_keys, config_sealed, nor any config key a
// checker declares secret or export-redacted: those keys are stripped from
// the config even when the server stores them in plaintext (no master key).
// Runtime state (status, counters, schedule) is not part of it either, and
// neither is the region list of an automatically placed check, which the
// scheduler rewrites on its own.
package checkversion

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/utils/timeutils"
)

// Snapshot is the versioned definition of a check. Field order is the
// canonical order: the hash is computed over json.Marshal of this struct,
// whose map keys encoding/json sorts.
type Snapshot struct {
	Name          string            `json:"name"`
	Slug          string            `json:"slug"`
	Description   string            `json:"description,omitempty"`
	Type          string            `json:"type"`
	Config        map[string]any    `json:"config"`
	CheckGroupUID string            `json:"checkGroupUid,omitempty"`
	Regions       []string          `json:"regions,omitempty"`
	Placement     string            `json:"placement,omitempty"`
	RegionCount   *int              `json:"regionCount,omitempty"`
	RegionPool    []string          `json:"regionPool,omitempty"`
	FailQuorum    string            `json:"failQuorum,omitempty"`
	Enabled       bool              `json:"enabled"`
	Period        string            `json:"period"`
	Labels        map[string]string `json:"labels,omitempty"`
}

// HiddenConfigKeys is the set of config keys a snapshot never carries: the
// checker's declared secrets, its export-redacted fields, and whatever the
// row advertises as private.
func HiddenConfigKeys(checkType string, configPrivateKeys *string) map[string]struct{} {
	hidden := map[string]struct{}{}

	if cfg, ok := configregistry.ParseConfig(checkerdef.CheckType(checkType)); ok {
		for _, key := range credentials.SecretFieldsFor(cfg) {
			hidden[key] = struct{}{}
		}

		for _, key := range credentials.ExportRedactedFieldsFor(cfg) {
			hidden[key] = struct{}{}
		}
	}

	if configPrivateKeys != nil && *configPrivateKeys != "" {
		var privateKeys []string
		if err := json.Unmarshal([]byte(*configPrivateKeys), &privateKeys); err == nil {
			for _, key := range privateKeys {
				hidden[key] = struct{}{}
			}
		}
	}

	return hidden
}

// Build returns the snapshot of a check and its labels.
func Build(check *models.Check, labels map[string]string) *Snapshot {
	hidden := HiddenConfigKeys(check.Type, check.ConfigPrivateKeys)

	config := make(map[string]any, len(check.Config))
	for key, value := range check.Config {
		if _, skip := hidden[key]; skip {
			continue
		}

		config[key] = value
	}

	snap := &Snapshot{
		Name:        deref(check.Name),
		Slug:        deref(check.Slug),
		Description: deref(check.Description),
		Type:        check.Type,
		Config:      config,
		Placement:   check.Placement,
		RegionCount: check.RegionCount,
		FailQuorum:  deref(check.FailQuorum),
		Enabled:     check.Enabled,
		Period:      FormatPeriod(check.Period),
	}

	if check.CheckGroupUID != nil {
		snap.CheckGroupUID = *check.CheckGroupUID
	}

	// An auto-placed check's region list is the scheduler's current
	// placement, not something a user wrote: its definition is the placement
	// mode, the region count and the pool.
	if check.Placement != models.PlacementAuto && len(check.Regions) > 0 {
		snap.Regions = append([]string(nil), check.Regions...)
	}

	if len(check.RegionPool) > 0 {
		snap.RegionPool = append([]string(nil), check.RegionPool...)
	}

	if len(labels) > 0 {
		snap.Labels = make(map[string]string, len(labels))
		for key, value := range labels {
			snap.Labels[key] = value
		}
	}

	return snap
}

// FormatPeriod renders a period the way the API accepts it back (HH:MM:SS).
func FormatPeriod(period timeutils.Duration) string {
	value, err := period.Value()
	if err != nil {
		return time.Duration(period).String()
	}

	text, _ := value.(string)

	return text
}

// Canonical returns the canonical JSON of the snapshot.
func (s *Snapshot) Canonical() ([]byte, error) {
	encoded, err := json.Marshal(s)
	if err != nil {
		return nil, fmt.Errorf("encoding check snapshot: %w", err)
	}

	return encoded, nil
}

// Hash returns the hex SHA-256 of the canonical JSON.
func (s *Snapshot) Hash() (string, error) {
	encoded, err := s.Canonical()
	if err != nil {
		return "", err
	}

	sum := sha256.Sum256(encoded)

	return hex.EncodeToString(sum[:]), nil
}

// ToJSONMap converts the snapshot into the stored column value.
func (s *Snapshot) ToJSONMap() (models.JSONMap, error) {
	encoded, err := s.Canonical()
	if err != nil {
		return nil, err
	}

	out := models.JSONMap{}
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, fmt.Errorf("decoding check snapshot: %w", err)
	}

	return out, nil
}

// FromJSONMap reads a stored snapshot back.
func FromJSONMap(stored models.JSONMap) (*Snapshot, error) {
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, fmt.Errorf("encoding stored snapshot: %w", err)
	}

	snap := &Snapshot{}
	if err := json.Unmarshal(encoded, snap); err != nil {
		return nil, fmt.Errorf("decoding stored snapshot: %w", err)
	}

	if snap.Config == nil {
		snap.Config = map[string]any{}
	}

	return snap, nil
}

// LabelMap turns label rows into a key/value map.
func LabelMap(labels []*models.Label) map[string]string {
	out := make(map[string]string, len(labels))
	for _, label := range labels {
		out[label.Key] = label.Value
	}

	return out
}

func deref(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}
