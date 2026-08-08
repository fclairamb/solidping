package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/uptrace/bun"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// applyDeviceAuthResolution adds the nullable consent columns to an update.
// An empty string becomes SQL NULL rather than an empty text value:
// organization_uid/user_uid are uuid columns on Postgres, where the empty
// string is not a valid uuid literal.
func applyDeviceAuthResolution(query *bun.UpdateQuery, res *models.DeviceAuthResolution) {
	query.Set("user_uid = ?", nullableString(res.UserUID))
	query.Set("organization_uid = ?", nullableString(res.OrganizationUID))
	query.Set("token_uid = ?", nullableString(res.TokenUID))
	query.Set("token_value = ?", nullableString(res.TokenValue))
}

// nullableString maps "" to a nil *string so bun writes SQL NULL.
func nullableString(value string) *string {
	if value == "" {
		return nil
	}

	return &value
}

// CreateDeviceAuthRequest inserts a new pending device-authorization request.
// A unique-violation on user_code is surfaced to the caller, which regenerates
// and retries.
func (s *Service) CreateDeviceAuthRequest(ctx context.Context, req *models.DeviceAuthRequest) error {
	if _, err := s.db.NewInsert().Model(req).Exec(ctx); err != nil {
		return fmt.Errorf("create device auth request: %w", err)
	}

	return nil
}

// GetDeviceAuthRequestByUserCode looks up a live request by its canonical
// (uppercase, dashless) user code.
func (s *Service) GetDeviceAuthRequestByUserCode(
	ctx context.Context, userCode string,
) (*models.DeviceAuthRequest, error) {
	req := new(models.DeviceAuthRequest)

	err := s.db.NewSelect().Model(req).
		Where("user_code = ?", userCode).
		Where("expires_at > ?", time.Now()).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get device auth request by user code: %w", err)
	}

	return req, nil
}

// GetDeviceAuthRequestByDeviceCode looks up a live request by the client's
// device code.
func (s *Service) GetDeviceAuthRequestByDeviceCode(
	ctx context.Context, deviceCode string,
) (*models.DeviceAuthRequest, error) {
	req := new(models.DeviceAuthRequest)

	err := s.db.NewSelect().Model(req).
		Where("device_code = ?", deviceCode).
		Where("expires_at > ?", time.Now()).
		Scan(ctx)
	if err != nil {
		return nil, fmt.Errorf("get device auth request by device code: %w", err)
	}

	return req, nil
}

// ResolveDeviceAuthRequest moves a live pending request to approved or denied.
// The status predicate makes it a compare-and-set: a losing concurrent
// responder gets false rather than overwriting the first decision.
func (s *Service) ResolveDeviceAuthRequest(
	ctx context.Context, uid string, res *models.DeviceAuthResolution,
) (bool, error) {
	query := s.db.NewUpdate().Model((*models.DeviceAuthRequest)(nil)).
		Set("status = ?", res.Status).
		Set("updated_at = ?", time.Now()).
		Where("uid = ?", uid).
		Where("status = ?", models.DeviceAuthStatusPending).
		Where("expires_at > ?", time.Now())

	applyDeviceAuthResolution(query, res)

	result, err := query.Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("resolve device auth request: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("resolve device auth request rows: %w", err)
	}

	return rows > 0, nil
}

// TouchDeviceAuthPoll stamps the last poll time used for slow_down enforcement.
func (s *Service) TouchDeviceAuthPoll(ctx context.Context, uid string, at time.Time) error {
	_, err := s.db.NewUpdate().Model((*models.DeviceAuthRequest)(nil)).
		Set("last_polled_at = ?", at).
		Where("uid = ?", uid).
		Exec(ctx)
	if err != nil {
		return fmt.Errorf("touch device auth poll: %w", err)
	}

	return nil
}

// ConsumeDeviceAuthRequest hard-deletes a resolved request. The returned
// boolean is the exactly-once gate: only the caller whose delete affected a row
// may hand the stashed PAT back to the client.
func (s *Service) ConsumeDeviceAuthRequest(ctx context.Context, uid string) (bool, error) {
	result, err := s.db.NewDelete().Model((*models.DeviceAuthRequest)(nil)).
		Where("uid = ?", uid).
		Exec(ctx)
	if err != nil {
		return false, fmt.Errorf("consume device auth request: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("consume device auth request rows: %w", err)
	}

	return rows > 0, nil
}

// PurgeExpiredDeviceAuthRequests deletes requests that expired before `before`.
func (s *Service) PurgeExpiredDeviceAuthRequests(ctx context.Context, before time.Time) (int64, error) {
	result, err := s.db.NewDelete().Model((*models.DeviceAuthRequest)(nil)).
		Where("expires_at <= ?", before).
		Exec(ctx)
	if err != nil {
		return 0, fmt.Errorf("purge expired device auth requests: %w", err)
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("purge expired device auth requests rows: %w", err)
	}

	return rows, nil
}
