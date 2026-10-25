// Package plugintypes stores the plugin check types an org's private-location
// agents reported (spec 2026-10-17-05 §18), so the check form and validation
// of those types work while the agent is offline.
//
// Every read takes the org: a type one org's agent reported is never visible
// to another org, in a listing, a schema or a validation.
package plugintypes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/plugins"
	"github.com/fclairamb/solidping/server/internal/plugins/protocol"
)

// Row is one stored report.
type Row struct {
	bun.BaseModel `bun:"table:org_plugin_types"`

	UID             string         `bun:"uid,pk,type:varchar(36)"`
	OrganizationUID string         `bun:"organization_uid,notnull"`
	CheckType       string         `bun:"check_type,notnull"`
	Manifest        models.JSONMap `bun:"manifest,type:jsonb,notnull"`
	CreatedAt       time.Time      `bun:"created_at,notnull,default:current_timestamp"`
	UpdatedAt       time.Time      `bun:"updated_at,notnull,default:current_timestamp"`
}

// Store reads and writes org_plugin_types.
type Store struct {
	db bun.IDB
}

// New returns a store on db.
func New(db bun.IDB) *Store {
	return &Store{db: db}
}

// Report stores the types an org's agent reported, replacing an earlier
// report of the same type. A type that does not compile, or that is
// built-in, is skipped with a log line. Returns how many were stored.
func (s *Store) Report(ctx context.Context, orgUID string, reported []protocol.TypeManifest) (int, error) {
	if orgUID == "" || len(reported) == 0 {
		return 0, nil
	}

	if len(reported) > plugins.MaxReportedTypes {
		reported = reported[:plugins.MaxReportedTypes]
	}

	now := time.Now()
	rows := make([]Row, 0, len(reported))
	seen := map[string]bool{}

	for i := range reported {
		manifest := &reported[i]

		if seen[manifest.Type] {
			continue
		}

		if _, err := plugins.CompileRemote(manifest); err != nil {
			slog.WarnContext(ctx, "Skipping a reported plugin type", "org_uid", orgUID, "type", manifest.Type, "error", err)

			continue
		}

		stored, err := toJSONMap(manifest)
		if err != nil {
			return 0, err
		}

		seen[manifest.Type] = true
		rows = append(rows, Row{
			UID: uuid.New().String(), OrganizationUID: orgUID, CheckType: manifest.Type,
			Manifest: stored, CreatedAt: now, UpdatedAt: now,
		})
	}

	if len(rows) == 0 {
		return 0, nil
	}

	_, err := s.db.NewInsert().Model(&rows).
		On("CONFLICT (organization_uid, check_type) DO UPDATE").
		Set("manifest = EXCLUDED.manifest").
		Set("updated_at = EXCLUDED.updated_at").
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("storing reported plugin types: %w", err)
	}

	return len(rows), nil
}

// Lookup returns one of the org's reported types, compiled. False when the
// org has no such type, or when its stored report no longer compiles.
func (s *Store) Lookup(ctx context.Context, orgUID, checkType string) (*plugins.RemoteType, bool, error) {
	if orgUID == "" || !checkerdef.CheckType(checkType).IsPlugin() {
		return nil, false, nil
	}

	var row Row

	err := s.db.NewSelect().Model(&row).
		Where("organization_uid = ?", orgUID).
		Where("check_type = ?", checkType).
		Limit(1).
		Scan(ctx)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}

	if err != nil {
		return nil, false, fmt.Errorf("reading plugin type %s: %w", checkType, err)
	}

	compiled, ok := compileRow(ctx, &row)

	return compiled, ok, nil
}

// List returns every type the org's agents reported, compiled, by type.
func (s *Store) List(ctx context.Context, orgUID string) ([]*plugins.RemoteType, error) {
	if orgUID == "" {
		return nil, nil
	}

	var rows []Row

	err := s.db.NewSelect().Model(&rows).
		Where("organization_uid = ?", orgUID).
		Order("check_type").
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing plugin types: %w", err)
	}

	out := make([]*plugins.RemoteType, 0, len(rows))

	for i := range rows {
		if compiled, ok := compileRow(ctx, &rows[i]); ok {
			out = append(out, compiled)
		}
	}

	return out, nil
}

func compileRow(ctx context.Context, row *Row) (*plugins.RemoteType, bool) {
	raw, err := json.Marshal(row.Manifest)
	if err != nil {
		return nil, false
	}

	var manifest protocol.TypeManifest
	if err = json.Unmarshal(raw, &manifest); err != nil || manifest.Type != row.CheckType {
		return nil, false
	}

	compiled, err := plugins.CompileRemote(&manifest)
	if err != nil {
		slog.WarnContext(ctx, "Stored plugin type no longer compiles", "type", row.CheckType, "error", err)

		return nil, false
	}

	return compiled, true
}

func toJSONMap(manifest *protocol.TypeManifest) (models.JSONMap, error) {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encoding plugin type %s: %w", manifest.Type, err)
	}

	var out models.JSONMap
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("encoding plugin type %s: %w", manifest.Type, err)
	}

	return out, nil
}
