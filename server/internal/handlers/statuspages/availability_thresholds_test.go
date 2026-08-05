package statuspages

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func f64(v float64) *float64 { return &v }

// TestAvailabilityToStatus_CustomThresholds is the positive control that
// defaults are unchanged when no page thresholds are configured, plus a
// pct that reads green under a lax page and amber/red under a strict page
// (spec 2026-08-03-01 §6).
func TestAvailabilityToStatus_CustomThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		pct               float64
		failures          int
		upThreshold       float64
		degradedThreshold float64
		want              string
	}{
		{
			name: "defaults unchanged: 99.95 is up", pct: 99.95, failures: 0,
			upThreshold: models.DefaultAvailabilityThresholdUp, degradedThreshold: models.DefaultAvailabilityThresholdDegraded,
			want: statusUp,
		},
		{
			name: "defaults unchanged: 99.5 is degraded", pct: 99.5, failures: 5,
			upThreshold: models.DefaultAvailabilityThresholdUp, degradedThreshold: models.DefaultAvailabilityThresholdDegraded,
			want: statusDegraded,
		},
		{
			name: "defaults unchanged: 98 with 5 failures is down", pct: 98, failures: 5,
			upThreshold: models.DefaultAvailabilityThresholdUp, degradedThreshold: models.DefaultAvailabilityThresholdDegraded,
			want: statusDownValue,
		},
		{
			name: "lax page: 99.5 reads up under a 99.0/98.0 page", pct: 99.5, failures: 0,
			upThreshold: 99.0, degradedThreshold: 98.0, want: statusUp,
		},
		{
			name: "strict page: same 99.5 reads degraded under a 99.9/99.5 page", pct: 99.5, failures: 5,
			upThreshold: 99.9, degradedThreshold: 99.5, want: statusDegraded,
		},
		{
			name: "strict page: 99.0 reads down (with enough failures) under a 99.9/99.5 page",
			pct:  99.0, failures: 5, upThreshold: 99.9, degradedThreshold: 99.5, want: statusDownValue,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := availabilityToStatus(tc.pct, tc.failures, tc.upThreshold, tc.degradedThreshold)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestAvailabilityToStatus_CalibrationGuard is the small-bucket calibration
// guard contract (spec 2026-08-03-01 §4): a bucket with exactly ONE failed
// sample never renders down, only at worst degraded; 2+ failures still go
// down when the pct is below the red line (the positive control proving the
// guard doesn't mask real reds).
func TestAvailabilityToStatus_CalibrationGuard(t *testing.T) {
	t.Parallel()

	const (
		up       = models.DefaultAvailabilityThresholdUp
		degraded = models.DefaultAvailabilityThresholdDegraded
	)

	tests := []struct {
		name     string
		up, down int // successful, failed counts
		want     string
	}{
		{name: "1 failure of 60 (hourly, 1-minute checks) renders degraded, not down", up: 59, down: 1, want: statusDegraded},
		{name: "2 failures of 60 renders down (positive control)", up: 58, down: 2, want: statusDownValue},
		{name: "0 failures renders up", up: 60, down: 0, want: statusUp},
		{name: "1 failure of 288 (daily, 5-minute checks) renders degraded", up: 287, down: 1, want: statusDegraded},
		{name: "3 failures of 288 renders down", up: 285, down: 3, want: statusDownValue},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			total := tc.up + tc.down
			pct := float64(tc.up) / float64(total) * 100

			got := availabilityToStatus(pct, tc.down, up, degraded)
			require.Equal(t, tc.want, got)
		})
	}
}

// TestValidateAvailabilitySettings covers the effective-value validation rule
// (0 < thresholdDegraded < thresholdUp <= 100), including the positive
// control that a nil section (all defaults) always passes.
func TestValidateAvailabilitySettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   *models.AvailabilitySettings
		wantErr bool
	}{
		{name: "nil section (defaults) is valid", input: nil},
		{name: "both nil fields (defaults) is valid", input: &models.AvailabilitySettings{}},
		{
			name:  "custom valid pair",
			input: &models.AvailabilitySettings{ThresholdUp: f64(99.5), ThresholdDegraded: f64(98)},
		},
		{
			name:    "degraded equal to up is rejected",
			input:   &models.AvailabilitySettings{ThresholdUp: f64(99), ThresholdDegraded: f64(99)},
			wantErr: true,
		},
		{
			name:    "degraded greater than up is rejected",
			input:   &models.AvailabilitySettings{ThresholdUp: f64(98), ThresholdDegraded: f64(99)},
			wantErr: true,
		},
		{
			name:    "degraded of 0 is rejected",
			input:   &models.AvailabilitySettings{ThresholdUp: f64(50), ThresholdDegraded: f64(0)},
			wantErr: true,
		},
		{
			name:    "up over 100 is rejected",
			input:   &models.AvailabilitySettings{ThresholdUp: f64(100.5), ThresholdDegraded: f64(50)},
			wantErr: true,
		},
		{
			name:  "up exactly 100 is valid",
			input: &models.AvailabilitySettings{ThresholdUp: f64(100), ThresholdDegraded: f64(50)},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateAvailabilitySettings(tc.input)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidAvailabilityThresholds)

				return
			}

			require.NoError(t, err)
		})
	}
}

// TestParseSettingsField covers the raw-body presence detection and strict
// (unknown-key-rejecting) decode that back the PATCH no-deep-merge semantics.
func TestParseSettingsField(t *testing.T) {
	t.Parallel()

	t.Run("absent settings key", func(t *testing.T) {
		t.Parallel()

		settings, present, err := parseSettingsField([]byte(`{"name":"x"}`))
		require.NoError(t, err)
		require.False(t, present)
		require.Nil(t, settings)
	})

	t.Run("explicit top-level null", func(t *testing.T) {
		t.Parallel()

		settings, present, err := parseSettingsField([]byte(`{"settings":null}`))
		require.NoError(t, err)
		require.True(t, present)
		require.Nil(t, settings)
	})

	t.Run("settings present, availability absent", func(t *testing.T) {
		t.Parallel()

		settings, present, err := parseSettingsField([]byte(`{"settings":{}}`))
		require.NoError(t, err)
		require.True(t, present)
		require.NotNil(t, settings)
		require.False(t, settings.AvailabilitySet)
	})

	t.Run("availability explicit null resets", func(t *testing.T) {
		t.Parallel()

		settings, present, err := parseSettingsField([]byte(`{"settings":{"availability":null}}`))
		require.NoError(t, err)
		require.True(t, present)
		require.NotNil(t, settings)
		require.True(t, settings.AvailabilitySet)
		require.Nil(t, settings.Availability)
	})

	t.Run("availability object replaces the section", func(t *testing.T) {
		t.Parallel()

		settings, present, err := parseSettingsField(
			[]byte(`{"settings":{"availability":{"thresholdUp":99.5,"thresholdDegraded":98}}}`))
		require.NoError(t, err)
		require.True(t, present)
		require.NotNil(t, settings)
		require.True(t, settings.AvailabilitySet)
		require.NotNil(t, settings.Availability)
		require.InDelta(t, 99.5, *settings.Availability.ThresholdUp, 0.001)
		require.InDelta(t, 98.0, *settings.Availability.ThresholdDegraded, 0.001)
	})

	t.Run("unknown top-level settings key is rejected", func(t *testing.T) {
		t.Parallel()

		_, _, err := parseSettingsField([]byte(`{"settings":{"legend":true}}`))
		require.ErrorIs(t, err, ErrSettingsUnknownField)
	})

	t.Run("unknown availability key is rejected", func(t *testing.T) {
		t.Parallel()

		_, _, err := parseSettingsField([]byte(`{"settings":{"availability":{"thresholdUpp":99}}}`))
		require.ErrorIs(t, err, ErrSettingsUnknownField)
	})
}

// TestUpdateStatusPage_SettingsPatchSemantics is the end-to-end PATCH
// contract on the service (spec 2026-08-03-01 §2): absent settings leaves the
// page untouched, an object replaces the availability section wholly,
// explicit null resets it, and an invalid effective pair is rejected.
func TestUpdateStatusPage_SettingsPatchSemantics(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Main", Slug: "main",
	})
	r.NoError(err)
	r.NotNil(page.Settings)
	r.Nil(page.Settings.Availability, "no thresholds set at create ⇒ nil section")
	r.InDelta(models.DefaultAvailabilityThresholdUp, page.AvailabilityThresholds.ThresholdUp, 0.001)
	r.InDelta(models.DefaultAvailabilityThresholdDegraded, page.AvailabilityThresholds.ThresholdDegraded, 0.001)

	// A PATCH not mentioning settings at all leaves it untouched.
	unrelated, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Name: strptr("Main Renamed"),
	})
	r.NoError(err)
	r.Nil(unrelated.Settings.Availability, "untouched by an update that doesn't mention settings")

	// A present "availability" object replaces the section wholly.
	set, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Settings: &SettingsInput{
			Availability:    &AvailabilitySettingsInput{ThresholdUp: f64(99.5), ThresholdDegraded: f64(98)},
			AvailabilitySet: true,
		},
		SettingsPresent: true,
	})
	r.NoError(err)
	r.NotNil(set.Settings.Availability)
	r.InDelta(99.5, *set.Settings.Availability.ThresholdUp, 0.001)
	r.InDelta(98.0, *set.Settings.Availability.ThresholdDegraded, 0.001)
	r.InDelta(99.5, set.AvailabilityThresholds.ThresholdUp, 0.001)
	r.InDelta(98.0, set.AvailabilityThresholds.ThresholdDegraded, 0.001)

	// A subsequent update that mentions "settings" but not "availability"
	// leaves the section as-is (sections not mentioned are untouched).
	untouched, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Settings:        &SettingsInput{},
		SettingsPresent: true,
	})
	r.NoError(err)
	r.NotNil(untouched.Settings.Availability)
	r.InDelta(99.5, *untouched.Settings.Availability.ThresholdUp, 0.001)

	// Explicit "availability": null resets the section to defaults.
	reset, err := svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Settings:        &SettingsInput{Availability: nil, AvailabilitySet: true},
		SettingsPresent: true,
	})
	r.NoError(err)
	r.Nil(reset.Settings.Availability)
	r.InDelta(models.DefaultAvailabilityThresholdUp, reset.AvailabilityThresholds.ThresholdUp, 0.001)

	// degraded >= up is rejected, and doesn't persist.
	_, err = svc.UpdateStatusPage(ctx, org.Slug, page.UID, &UpdateStatusPageRequest{
		Settings: &SettingsInput{
			Availability:    &AvailabilitySettingsInput{ThresholdUp: f64(98), ThresholdDegraded: f64(99)},
			AvailabilitySet: true,
		},
		SettingsPresent: true,
	})
	r.ErrorIs(err, ErrInvalidAvailabilityThresholds)

	after, err := svc.GetStatusPage(ctx, org.Slug, page.UID, GetStatusPageOptions{})
	r.NoError(err)
	r.Nil(after.Settings.Availability, "rejected update must not persist")
}

// TestCreateStatusPage_Settings covers the create path: settings round-trips
// onto the response and an invalid effective pair is rejected before the row
// is ever written.
func TestCreateStatusPage_Settings(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	page, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug,
		Settings: &SettingsInput{
			Availability:    &AvailabilitySettingsInput{ThresholdUp: f64(99.99), ThresholdDegraded: f64(99.5)},
			AvailabilitySet: true,
		},
	})
	r.NoError(err)
	r.NotNil(page.Settings.Availability)
	r.InDelta(99.99, *page.Settings.Availability.ThresholdUp, 0.001)
	r.InDelta(99.5, page.AvailabilityThresholds.ThresholdDegraded, 0.001)

	_, err = svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Broken", Slug: "broken",
		Settings: &SettingsInput{
			Availability:    &AvailabilitySettingsInput{ThresholdUp: f64(50), ThresholdDegraded: f64(60)},
			AvailabilitySet: true,
		},
	})
	r.ErrorIs(err, ErrInvalidAvailabilityThresholds)
}

// TestViewStatusPage_ExposesEffectiveThresholds is the public-payload
// contract (spec 2026-08-03-01 §2): the public response always carries the
// resolved, non-nil thresholds, even when the page has never set any.
func TestViewStatusPage_ExposesEffectiveThresholds(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx, svc, org := setupStatusPagesTest(t)

	_, err := svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{
		Name: "Public", Slug: testPublicSlug,
		Settings: &SettingsInput{
			Availability:    &AvailabilitySettingsInput{ThresholdUp: f64(99.5), ThresholdDegraded: f64(98)},
			AvailabilitySet: true,
		},
	})
	r.NoError(err)

	view, err := svc.ViewStatusPage(ctx, org.Slug, testPublicSlug)
	r.NoError(err)
	r.InDelta(99.5, view.AvailabilityThresholds.ThresholdUp, 0.001)
	r.InDelta(98.0, view.AvailabilityThresholds.ThresholdDegraded, 0.001)

	// A page that never set thresholds still exposes the (default) resolved
	// values — never a nil/omitted field.
	_, err = svc.CreateStatusPage(ctx, org.Slug, &CreateStatusPageRequest{Name: "Bare", Slug: "bare"})
	r.NoError(err)

	bareView, err := svc.ViewStatusPage(ctx, org.Slug, "bare")
	r.NoError(err)
	r.InDelta(models.DefaultAvailabilityThresholdUp, bareView.AvailabilityThresholds.ThresholdUp, 0.001)
	r.InDelta(models.DefaultAvailabilityThresholdDegraded, bareView.AvailabilityThresholds.ThresholdDegraded, 0.001)
}
