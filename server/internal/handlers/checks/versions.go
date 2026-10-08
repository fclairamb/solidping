package checks

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/audit"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkversion"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/dbctx"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/regionquorum"
)

// Check version history (spec 2026-10-03-06): list, read, diff, restore, and
// the approve/reject decision on proposed versions.

var (
	// ErrCheckVersionNotFound is returned when the check has no such version.
	ErrCheckVersionNotFound = errors.New("check version not found")
	// ErrCheckVersionNotApplied is returned when restoring a version that was
	// never live (a proposal or a rejected one).
	ErrCheckVersionNotApplied = errors.New("only an applied version can be restored")
	// ErrCheckVersionNotProposed is returned when approving or rejecting a
	// version that is not a pending proposal.
	ErrCheckVersionNotProposed = errors.New("check version is not a pending proposal")
	// ErrCheckVersionStale is returned when approving a proposal built on an
	// applied version that is no longer the latest.
	ErrCheckVersionStale = errors.New("the proposal was built on an older version of the check")
)

// maxCheckVersionsLimit caps ?limit on the version list (the retention is 100
// applied versions, plus proposals).
const maxCheckVersionsLimit = 500

// CheckVersionResponse is one version of a check on the wire. Snapshot is set
// only when a single version is read.
type CheckVersionResponse struct {
	Version          int                    `json:"version"`
	Status           string                 `json:"status"`
	Origin           string                 `json:"origin"`
	ActorUserUID     *string                `json:"actorUserUid,omitempty"`
	ActorName        *string                `json:"actorName,omitempty"`
	Reason           *string                `json:"reason,omitempty"`
	BaseVersion      *int                   `json:"baseVersion,omitempty"`
	DecidedByUserUID *string                `json:"decidedByUserUid,omitempty"`
	DecidedAt        *time.Time             `json:"decidedAt,omitempty"`
	CreatedAt        time.Time              `json:"createdAt"`
	Snapshot         *checkversion.Snapshot `json:"snapshot,omitempty"`
}

// ListCheckVersionsResponse wraps the version list.
type ListCheckVersionsResponse struct {
	Data []CheckVersionResponse `json:"data"`
}

// CheckVersionDiffResponse is the field diff between two versions. Against
// is nil when the version has nothing before it (the first version).
type CheckVersionDiffResponse struct {
	Version int                `json:"version"`
	Against *int               `json:"against"`
	Changes []CheckFieldChange `json:"changes"`
}

// withCallerChangeSource puts a change source naming the authenticated caller
// on the context, unless an entry point (apply, MCP) already set one. A
// dashboard session records origin `user`, an API token `api`. No caller
// leaves the context alone, which the DB layer records as `system`.
func withCallerChangeSource(ctx context.Context) context.Context {
	if dbctx.ChangeSourceFromContext(ctx) != nil {
		return ctx
	}

	userUID, origin := callerChangeIdentity(ctx)
	if userUID == "" {
		return ctx
	}

	return dbctx.WithChangeSource(ctx, string(origin), userUID)
}

func callerChangeIdentity(ctx context.Context) (string, models.CheckVersionOrigin) {
	actor := audit.ActorFromContext(ctx)

	userUID := callerFromContext(ctx).userUID
	if userUID == "" {
		userUID = actor.UserUID
	}

	if actor.Type == models.ActorTypeAPIToken {
		return userUID, models.CheckVersionOriginAPI
	}

	return userUID, models.CheckVersionOriginUser
}

// markAIGenerated switches the change's origin to ai_generate when the write
// saves a freshly generated js script: an `ai` block whose generated_at
// differs from the stored one (spec 2026-10-03-07). Only a caller-attributed
// change (user, api) is relabeled; the actor stays the caller.
func markAIGenerated(ctx context.Context, checkType string, config, stored map[string]any) {
	source := dbctx.ChangeSourceFromContext(ctx)
	// A restore carries its own reason ("restored vN") and keeps its origin.
	if source == nil || source.Reason != "" || checkType != string(checkerdef.CheckTypeJS) {
		return
	}

	if source.Origin != string(models.CheckVersionOriginUser) && source.Origin != string(models.CheckVersionOriginAPI) {
		return
	}

	generatedAt := aiGeneratedAt(config)
	if generatedAt == "" || generatedAt == aiGeneratedAt(stored) {
		return
	}

	source.Origin = string(models.CheckVersionOriginAIGenerate)
}

func aiGeneratedAt(config map[string]any) string {
	block, _ := config["ai"].(map[string]any)
	value, _ := block["generated_at"].(string)

	return value
}

// WithChangeOrigin starts a new change on the context with the given origin
// and the authenticated caller as the actor. Used by the config-as-code and
// MCP entry points.
func WithChangeOrigin(ctx context.Context, origin models.CheckVersionOrigin) context.Context {
	userUID, _ := callerChangeIdentity(ctx)

	return dbctx.WithChangeSource(ctx, string(origin), userUID)
}

func (s *Service) resolveVersionedCheck(ctx context.Context, orgSlug, identifier string) (*models.Check, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	check, err := s.db.GetCheckByUidOrSlug(ctx, org.UID, identifier)
	if err != nil || check == nil {
		return nil, ErrCheckNotFound
	}

	return check, nil
}

func (s *Service) loadCheckVersion(ctx context.Context, checkUID string, version int) (*models.CheckVersion, error) {
	row, err := s.db.GetCheckVersion(ctx, checkUID, version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrCheckVersionNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("loading check version: %w", err)
	}

	return row, nil
}

// ListCheckVersions returns a check's versions, newest first, without their
// snapshots.
func (s *Service) ListCheckVersions(
	ctx context.Context, orgSlug, identifier string, limit int,
) (*ListCheckVersionsResponse, error) {
	check, err := s.resolveVersionedCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	if limit <= 0 || limit > maxCheckVersionsLimit {
		limit = maxCheckVersionsLimit
	}

	rows, err := s.db.ListCheckVersions(ctx, check.UID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing check versions: %w", err)
	}

	names := s.actorNames(ctx, rows)

	out := &ListCheckVersionsResponse{Data: make([]CheckVersionResponse, 0, len(rows))}
	for _, row := range rows {
		out.Data = append(out.Data, versionResponse(row, names))
	}

	return out, nil
}

// GetCheckVersion returns one version with its snapshot.
func (s *Service) GetCheckVersion(
	ctx context.Context, orgSlug, identifier string, version int,
) (*CheckVersionResponse, error) {
	check, err := s.resolveVersionedCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	row, err := s.loadCheckVersion(ctx, check.UID, version)
	if err != nil {
		return nil, err
	}

	snap, err := redactedSnapshot(row)
	if err != nil {
		return nil, err
	}

	response := versionResponse(row, s.actorNames(ctx, []*models.CheckVersion{row}))
	response.Snapshot = snap

	return &response, nil
}

// DiffCheckVersion diffs a version against another one, by default the
// applied version before it.
func (s *Service) DiffCheckVersion(
	ctx context.Context, orgSlug, identifier string, version int, against *int,
) (*CheckVersionDiffResponse, error) {
	check, err := s.resolveVersionedCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	row, err := s.loadCheckVersion(ctx, check.UID, version)
	if err != nil {
		return nil, err
	}

	target, err := redactedSnapshot(row)
	if err != nil {
		return nil, err
	}

	if against == nil {
		against, err = s.previousAppliedVersion(ctx, check.UID, version)
		if err != nil {
			return nil, err
		}
	}

	from := &checkversion.Snapshot{}

	if against != nil {
		other, otherErr := s.loadCheckVersion(ctx, check.UID, *against)
		if otherErr != nil {
			return nil, otherErr
		}

		if from, err = redactedSnapshot(other); err != nil {
			return nil, err
		}
	}

	return &CheckVersionDiffResponse{
		Version: version,
		Against: against,
		Changes: diffSnapshots(from, target),
	}, nil
}

func (s *Service) previousAppliedVersion(ctx context.Context, checkUID string, version int) (*int, error) {
	rows, err := s.db.ListCheckVersions(ctx, checkUID, 0)
	if err != nil {
		return nil, fmt.Errorf("listing check versions: %w", err)
	}

	for _, row := range rows { // newest first
		if row.Version < version && row.Status == models.CheckVersionStatusApplied {
			previous := row.Version

			return &previous, nil
		}
	}

	return nil, nil //nolint:nilnil // no earlier applied version: diff against nothing
}

// RestoreCheckVersion applies an applied version's definition again through
// UpdateCheck, which records it as a new version. Secrets are not versioned:
// the check keeps its current ones.
func (s *Service) RestoreCheckVersion(
	ctx context.Context, orgSlug, identifier string, version int,
) (CheckResponse, error) {
	check, err := s.resolveVersionedCheck(ctx, orgSlug, identifier)
	if err != nil {
		return CheckResponse{}, err
	}

	row, err := s.loadCheckVersion(ctx, check.UID, version)
	if err != nil {
		return CheckResponse{}, err
	}

	if row.Status != models.CheckVersionStatusApplied {
		return CheckResponse{}, ErrCheckVersionNotApplied
	}

	userUID, origin := callerChangeIdentity(ctx)
	source := &dbctx.ChangeSource{
		Origin:  string(origin),
		UserUID: userUID,
		Reason:  fmt.Sprintf("restored v%d", version),
	}

	return s.applySnapshot(dbctx.WithChange(ctx, source), orgSlug, check, row)
}

// ApproveCheckVersion applies a proposed version. The proposal row itself
// becomes the applied version. A proposal built on an applied version that is
// no longer the latest is refused (ErrCheckVersionStale).
func (s *Service) ApproveCheckVersion(
	ctx context.Context, orgSlug, identifier string, version int,
) (CheckResponse, error) {
	check, err := s.resolveVersionedCheck(ctx, orgSlug, identifier)
	if err != nil {
		return CheckResponse{}, err
	}

	row, err := s.loadCheckVersion(ctx, check.UID, version)
	if err != nil {
		return CheckResponse{}, err
	}

	if row.Status != models.CheckVersionStatusProposed {
		return CheckResponse{}, ErrCheckVersionNotProposed
	}

	latest, err := s.db.GetLatestAppliedCheckVersion(ctx, check.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("reading the latest applied version: %w", err)
	}

	if row.BaseVersion == nil || latest == nil || *row.BaseVersion != latest.Version {
		return CheckResponse{}, ErrCheckVersionStale
	}

	// The proposal keeps its own origin and author; the approver is recorded
	// as the decider.
	source := &dbctx.ChangeSource{Origin: string(row.Origin)}
	if row.ActorUserUID != nil {
		source.UserUID = *row.ActorUserUID
	}

	source.SetRecordedVersion(check.UID, row.Version)

	response, err := s.applySnapshot(dbctx.WithChange(ctx, source), orgSlug, check, row)
	if err != nil {
		return CheckResponse{}, err
	}

	approver, _ := callerChangeIdentity(ctx)
	if err := s.db.DecideCheckVersion(
		ctx, check.UID, row.Version, models.CheckVersionStatusApplied, approver,
	); err != nil {
		return CheckResponse{}, fmt.Errorf("recording the approval: %w", err)
	}

	return response, nil
}

// RejectCheckVersion marks a proposal rejected. The check is not touched.
func (s *Service) RejectCheckVersion(
	ctx context.Context, orgSlug, identifier string, version int,
) (*CheckVersionResponse, error) {
	check, err := s.resolveVersionedCheck(ctx, orgSlug, identifier)
	if err != nil {
		return nil, err
	}

	if _, loadErr := s.loadCheckVersion(ctx, check.UID, version); loadErr != nil {
		return nil, loadErr
	}

	decider, _ := callerChangeIdentity(ctx)

	err = s.db.DecideCheckVersion(ctx, check.UID, version, models.CheckVersionStatusRejected, decider)
	if errors.Is(err, db.ErrCheckVersionNotProposed) {
		return nil, ErrCheckVersionNotProposed
	}

	if err != nil {
		return nil, fmt.Errorf("rejecting check version: %w", err)
	}

	row, err := s.loadCheckVersion(ctx, check.UID, version)
	if err != nil {
		return nil, err
	}

	response := versionResponse(row, s.actorNames(ctx, []*models.CheckVersion{row}))

	return &response, nil
}

// applySnapshot turns a stored snapshot into a PATCH of the fields that
// differ from the check's current definition, and runs it through the
// regular UpdateCheck path (validation, sealing, job reconciliation, labels).
// Config keys absent from the snapshot are the secret and export-redacted
// ones, which the PATCH merge preserves.
func (s *Service) applySnapshot(
	ctx context.Context, orgSlug string, check *models.Check, row *models.CheckVersion,
) (CheckResponse, error) {
	target, err := checkversion.FromJSONMap(row.Snapshot)
	if err != nil {
		return CheckResponse{}, err
	}

	labels, err := s.db.GetLabelsForCheck(ctx, check.UID)
	if err != nil {
		return CheckResponse{}, fmt.Errorf("failed to get labels: %w", err)
	}

	current := checkversion.Build(check, checkversion.LabelMap(labels))
	req := snapshotPatch(current, target)

	return s.UpdateCheck(ctx, orgSlug, check.UID, req)
}

func snapshotPatch(current, target *checkversion.Snapshot) *UpdateCheckRequest {
	req := &UpdateCheckRequest{}

	if current.Name != target.Name && target.Name != "" {
		req.Name = &target.Name
	}

	if current.Slug != target.Slug && target.Slug != "" {
		req.Slug = &target.Slug
	}

	if current.Description != target.Description {
		req.Description = &target.Description
	}

	if current.CheckGroupUID != target.CheckGroupUID {
		group := target.CheckGroupUID
		req.CheckGroupUID = &group
	}

	if !reflect.DeepEqual(canonicalJSON(current.Config), canonicalJSON(target.Config)) {
		config := maps.Clone(target.Config)
		req.Config = &config
	}

	if current.Enabled != target.Enabled {
		req.Enabled = &target.Enabled
	}

	if current.Period != target.Period && target.Period != "" {
		req.Period = &target.Period
	}

	if current.FailQuorum != target.FailQuorum {
		value := regionquorum.Value(regionquorum.KeywordDefault)
		if target.FailQuorum != "" {
			value = regionquorum.Value(target.FailQuorum)
		}

		req.FailQuorum = &value
	}

	applyPlacementPatch(req, current, target)

	if !maps.Equal(current.Labels, target.Labels) {
		labels := maps.Clone(target.Labels)
		if labels == nil {
			labels = map[string]string{}
		}

		req.Labels = &labels
	}

	return req
}

func applyPlacementPatch(req *UpdateCheckRequest, current, target *checkversion.Snapshot) {
	samePlacement := current.Placement == target.Placement &&
		slices.Equal(current.RegionPool, target.RegionPool) &&
		intPtrEqual(current.RegionCount, target.RegionCount)

	if target.Placement == models.PlacementAuto {
		if samePlacement {
			return
		}

		placement := models.PlacementAuto
		req.Placement = &placement
		req.RegionCount = target.RegionCount
		pool := append([]string{}, target.RegionPool...)
		req.RegionPool = &pool

		return
	}

	if samePlacement && slices.Equal(current.Regions, target.Regions) {
		return
	}

	placement := models.PlacementPinned
	req.Placement = &placement
	regions := append([]string{}, target.Regions...)
	req.Regions = &regions
}

func intPtrEqual(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}

func canonicalJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%v", value)
	}

	return string(encoded)
}

// redactedSnapshot reads a stored snapshot and strips the config keys GET on
// a check would never show in an export. Snapshots are written without them,
// so this only matters for a type whose secret set grew since.
func redactedSnapshot(row *models.CheckVersion) (*checkversion.Snapshot, error) {
	snap, err := checkversion.FromJSONMap(row.Snapshot)
	if err != nil {
		return nil, err
	}

	for key := range checkversion.HiddenConfigKeys(snap.Type, nil) {
		delete(snap.Config, key)
	}

	return snap, nil
}

// diffSnapshots lists the fields that differ between two snapshots. Config
// and labels diff per key. Values carrying a ${...} reference are masked.
func diffSnapshots(from, target *checkversion.Snapshot) []CheckFieldChange {
	changes := []CheckFieldChange{}

	add := func(field, before, after string) {
		if before != after {
			changes = append(changes, CheckFieldChange{
				Field: field, From: maskReferences(before), To: maskReferences(after),
			})
		}
	}

	add("name", from.Name, target.Name)
	add("slug", from.Slug, target.Slug)
	add("description", from.Description, target.Description)
	add("type", from.Type, target.Type)
	add("checkGroupUid", from.CheckGroupUID, target.CheckGroupUID)
	// Nothing before the first version: "enabled: false -> true" would be noise.
	enabledBefore := strconv.FormatBool(from.Enabled)
	if from.Type == "" {
		enabledBefore = ""
	}

	add("enabled", enabledBefore, strconv.FormatBool(target.Enabled))
	add("period", from.Period, target.Period)
	add("placement", from.Placement, target.Placement)
	add("regions", strings.Join(from.Regions, ","), strings.Join(target.Regions, ","))
	add("regionCount", intPtrString(from.RegionCount), intPtrString(target.RegionCount))
	add("regionPool", strings.Join(from.RegionPool, ","), strings.Join(target.RegionPool, ","))
	add("failQuorum", from.FailQuorum, target.FailQuorum)

	for _, key := range unionKeys(from.Config, target.Config) {
		add("config."+key, canonicalConfigValue(from.Config[key]), canonicalConfigValue(target.Config[key]))
	}

	for _, key := range unionKeys(from.Labels, target.Labels) {
		add("labels."+key, from.Labels[key], target.Labels[key])
	}

	return changes
}

func unionKeys[V any](a, b map[string]V) []string {
	keys := make([]string, 0, len(a))
	seen := map[string]struct{}{}

	for _, m := range []map[string]V{a, b} {
		for key := range m {
			if _, ok := seen[key]; ok {
				continue
			}

			seen[key] = struct{}{}
			keys = append(keys, key)
		}
	}

	sort.Strings(keys)

	return keys
}

func versionResponse(row *models.CheckVersion, names map[string]string) CheckVersionResponse {
	response := CheckVersionResponse{
		Version:          row.Version,
		Status:           string(row.Status),
		Origin:           string(row.Origin),
		ActorUserUID:     row.ActorUserUID,
		Reason:           row.Reason,
		BaseVersion:      row.BaseVersion,
		DecidedByUserUID: row.DecidedByUID,
		DecidedAt:        row.DecidedAt,
		CreatedAt:        row.CreatedAt,
	}

	if row.ActorUserUID != nil {
		if name, ok := names[*row.ActorUserUID]; ok {
			response.ActorName = &name
		}
	}

	return response
}

// actorNames resolves the display name (or email) of every actor in rows.
func (s *Service) actorNames(ctx context.Context, rows []*models.CheckVersion) map[string]string {
	names := map[string]string{}

	for _, row := range rows {
		if row.ActorUserUID == nil {
			continue
		}

		uid := *row.ActorUserUID
		if _, done := names[uid]; done {
			continue
		}

		user, err := s.db.GetUser(ctx, uid)
		if err != nil || user == nil {
			names[uid] = ""

			continue
		}

		names[uid] = user.Name
		if names[uid] == "" {
			names[uid] = user.Email
		}
	}

	for uid, name := range names {
		if name == "" {
			delete(names, uid)
		}
	}

	return names
}
