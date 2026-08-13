package checks_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

const testFingerprint = "SHA256:47DEQpj8HBSa+/TImW+5JCeuQeRkm5NMpJWZG3hSuFU="

func setupTunnelChecksService(t *testing.T) (*checks.Service, db.Service, *models.Organization) {
	t.Helper()

	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(ctx))

	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("tunnel-test", "Tunnel Test Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	// A real credentials service: the SSH bastions below carry a password, so
	// their config genuinely goes through the encrypt-at-rest split.
	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	require.NoError(t, err)

	return checks.NewService(dbSvc, nil, creds, nil), dbSvc, org
}

// createBastion creates an SSH check usable as a tunnel (fingerprint set).
func createBastion(t *testing.T, svc *checks.Service, ctx context.Context, //nolint:revive
	org *models.Organization, slug string,
) checks.CheckResponse {
	t.Helper()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: slug, Slug: slug, Type: "ssh",
		Config: map[string]any{
			"host":                 "bastion.example.com",
			"expected_fingerprint": testFingerprint,
			"username":             "probe",
			"password":             "s3cret",
		},
	})
	require.NoError(t, err)

	return created
}

func TestCreateCheckWithTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createBastion(t, svc, ctx, org, "bastion")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "private-api", Slug: "private-api", Type: "http",
		Config: map[string]any{
			"url":                              "http://internal.private/health",
			checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
		},
	})
	r.NoError(err)

	// The reference stays on the PUBLIC side of the config split — it is not a
	// secret, and the delete guard queries it as JSON.
	r.Equal(bastion.UID, created.Config[checkerdef.TunnelCheckUIDConfigKey])
}

func TestCreateCheckTunnelValidationRejections(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createBastion(t, svc, ctx, org, "bastion")

	// An SSH check with no fingerprint: perfectly valid as a check, but it
	// cannot be a tunnel — the tunnel carries the probe's traffic, so an
	// unverified bastion is a silent MITM.
	noFingerprint, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "unverified", Slug: "unverified", Type: "ssh",
		Config: map[string]any{"host": "bastion.example.com"},
	})
	require.NoError(t, err)

	// A plain HTTP check, to be referenced as if it were a bastion.
	notSSH, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "plain", Slug: "plain", Type: "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	require.NoError(t, err)

	// Another org's SSH check must be invisible: the lookup is org-scoped, so a
	// cross-org reference can never become a tunnel into someone else's network.
	otherOrgBastion := createBastionInOtherOrg(t, svc, dbSvc, ctx)

	tests := []struct {
		name      string
		checkType string
		config    map[string]any
		wantErr   string
	}{
		{
			name: "type does not support tunneling", checkType: "icmp",
			config: map[string]any{
				"host":                             "10.0.0.1",
				checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
			},
			wantErr: "cannot run through an SSH tunnel",
		},
		{
			name: "referenced check does not exist", checkType: "http",
			config: map[string]any{
				"url":                              "https://example.com",
				checkerdef.TunnelCheckUIDConfigKey: "00000000-0000-0000-0000-000000000000",
			},
			wantErr: "does not exist in this organization",
		},
		{
			name: "referenced check is in another org", checkType: "http",
			config: map[string]any{
				"url":                              "https://example.com",
				checkerdef.TunnelCheckUIDConfigKey: otherOrgBastion,
			},
			wantErr: "does not exist in this organization",
		},
		{
			name: "referenced check is not ssh", checkType: "http",
			config: map[string]any{
				"url":                              "https://example.com",
				checkerdef.TunnelCheckUIDConfigKey: notSSH.UID,
			},
			wantErr: "only ssh checks can be used as a tunnel",
		},
		{
			name: "referenced ssh check has no fingerprint", checkType: "http",
			config: map[string]any{
				"url":                              "https://example.com",
				checkerdef.TunnelCheckUIDConfigKey: noFingerprint.UID,
			},
			wantErr: "must set expected_fingerprint",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
				Name: tt.name, Type: tt.checkType, Config: tt.config,
			})
			r.Error(err)
			r.Contains(err.Error(), tt.wantErr)

			// Field-level ConfigError → the handler renders a 400 naming the
			// tunnel field, so the dashboard can attach it to its selector.
			configErr := checkerdef.IsConfigError(err)
			r.NotNil(configErr)
			r.Equal(checkerdef.TunnelCheckUIDConfigKey, configErr.Parameter)
		})
	}
}

// Chaining is rejected: it is what makes cycles impossible to build in v1.
// The rule has to be enforced against a stored chained reference, which only
// PATCH can produce (create validates the type first).
func TestTunnelChainingIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, dbSvc, org := setupTunnelChecksService(t)
	ctx := t.Context()

	first := createBastion(t, svc, ctx, org, "bastion-one")
	second := createBastion(t, svc, ctx, org, "bastion-two")

	// Plant a chained reference directly: the API refuses to create one (ssh
	// has no SupportsTunnel), so this simulates a hand-edited / legacy row and
	// proves the rule is enforced at use time, not only at write time.
	row, err := dbSvc.GetCheck(ctx, org.UID, second.UID)
	r.NoError(err)
	row.Config[checkerdef.TunnelCheckUIDConfigKey] = first.UID
	config := row.Config
	r.NoError(dbSvc.UpdateCheck(ctx, second.UID, &models.CheckUpdate{Config: &config}))

	_, err = svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "chained", Slug: "chained", Type: "http",
		Config: map[string]any{
			"url":                              "https://example.com",
			checkerdef.TunnelCheckUIDConfigKey: second.UID,
		},
	})
	r.Error(err)
	r.Contains(err.Error(), "chained tunnels are not supported")
}

// UpdateCheck never calls checker.Validate, so the service-level rule is the
// ONLY gate on the PATCH path — a dangling reference must not sneak in there.
func TestUpdateCheckValidatesTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "api", Slug: "api", Type: "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)

	_, err = svc.UpdateCheck(ctx, org.Slug, *created.Slug, &checks.UpdateCheckRequest{
		Config: &map[string]any{
			checkerdef.TunnelCheckUIDConfigKey: "00000000-0000-0000-0000-000000000000",
		},
	})
	r.Error(err)
	r.Contains(err.Error(), "does not exist in this organization")
}

// Editing an unrelated field must not drop the tunnel reference.
//
// The two shapes below are deliberate: a PATCH that does not touch `config` at all
// leaves the stored config (tunnel reference included) untouched, and a PATCH
// that does send `config` replaces the public side wholesale — the long-standing
// contract every other public key (`timeout`, `method`, `headers`…) already
// follows. The tunnel key is NOT special-cased into being preserved; that is
// what lets the dashboard's "None" clear it by simply omitting it.
func TestUpdateUnrelatedFieldPreservesTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createBastion(t, svc, ctx, org, "bastion")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "private-api", Slug: "private-api", Type: "http",
		Config: map[string]any{
			"url":                              "http://internal.private/health",
			checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
		},
	})
	r.NoError(err)

	// 1. A config-less PATCH (rename) must not disturb the tunnel.
	newName := "private-api-renamed"
	renamed, err := svc.UpdateCheck(ctx, org.Slug, *created.Slug, &checks.UpdateCheckRequest{
		Name: &newName,
	})
	r.NoError(err)
	r.Equal(bastion.UID, renamed.Config[checkerdef.TunnelCheckUIDConfigKey])

	// 2. A config PATCH carrying the tunnel key alongside the edited field
	// keeps it — this is the shape the dashboard actually sends.
	updated, err := svc.UpdateCheck(ctx, org.Slug, *created.Slug, &checks.UpdateCheckRequest{
		Config: &map[string]any{
			"url":                              "http://internal.private/healthz",
			checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
		},
	})
	r.NoError(err)
	r.Equal("http://internal.private/healthz", updated.Config["url"])
	r.Equal(bastion.UID, updated.Config[checkerdef.TunnelCheckUIDConfigKey])
}

// Selecting "None" in the dashboard omits the key from the submitted config,
// which detaches the tunnel under the wholesale-replace rule — and the bastion
// then becomes deletable again.
func TestUpdateCanClearTunnel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createBastion(t, svc, ctx, org, "bastion")

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "private-api", Slug: "private-api", Type: "http",
		Config: map[string]any{
			"url":                              "http://internal.private/health",
			checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
		},
	})
	r.NoError(err)

	updated, err := svc.UpdateCheck(ctx, org.Slug, *created.Slug, &checks.UpdateCheckRequest{
		Config: &map[string]any{"url": "http://internal.private/health"},
	})
	r.NoError(err)
	r.Empty(updated.Config[checkerdef.TunnelCheckUIDConfigKey])

	// A detached check can now be deleted without the guard firing.
	r.NoError(svc.DeleteCheck(ctx, org.Slug, *bastion.Slug))
}

// Deleting a bastion other checks tunnel through must 409 and name them —
// otherwise every dependent silently starts failing on its next execution.
func TestDeleteBastionInUseIsRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createBastion(t, svc, ctx, org, "bastion")

	for _, slug := range []string{"private-api", "private-db"} {
		_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
			Name: slug, Slug: slug, Type: "http",
			Config: map[string]any{
				"url":                              "http://internal.private/",
				checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
			},
		})
		r.NoError(err)
	}

	err := svc.DeleteCheck(ctx, org.Slug, *bastion.Slug)
	r.Error(err)
	r.ErrorIs(err, checks.ErrTunnelInUse)
	// The dependents are named so the user knows what to detach.
	r.Contains(err.Error(), "private-api")
	r.Contains(err.Error(), "private-db")

	// The bastion is still there.
	_, getErr := svc.GetCheck(ctx, org.Slug, *bastion.Slug, checks.GetCheckOptions{})
	r.NoError(getErr)
}

// The guard must not fire for unrelated checks: only a real dependent blocks.
func TestDeleteBastionWithoutDependentsSucceeds(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createBastion(t, svc, ctx, org, "bastion")
	other := createBastion(t, svc, ctx, org, "other-bastion")

	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "via-other", Slug: "via-other", Type: "http",
		Config: map[string]any{
			"url":                              "http://internal.private/",
			checkerdef.TunnelCheckUIDConfigKey: other.UID,
		},
	})
	r.NoError(err)

	r.NoError(svc.DeleteCheck(ctx, org.Slug, *bastion.Slug))
}

// A deleted dependent must stop blocking its bastion's deletion.
func TestDeletedDependentDoesNotBlockBastion(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createBastion(t, svc, ctx, org, "bastion")

	dependent, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "private-api", Slug: "private-api", Type: "http",
		Config: map[string]any{
			"url":                              "http://internal.private/",
			checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
		},
	})
	r.NoError(err)

	r.ErrorIs(svc.DeleteCheck(ctx, org.Slug, *bastion.Slug), checks.ErrTunnelInUse)
	r.NoError(svc.DeleteCheck(ctx, org.Slug, *dependent.Slug))
	r.NoError(svc.DeleteCheck(ctx, org.Slug, *bastion.Slug))
}

// parisRegion / londonRegion / cloudRegion are the region strings the region
// tests below allocate checks to. Private regions are stored fully-qualified
// (`@<org>/<slug>`); the tests never enroll agents, so the SSH bastions they
// build stay mixed-region (a cloud region alongside the private ones) which
// keeps them server-resolvable and out of the "no agents to seal to" path.
const (
	// Org-relative since spec 2026-08-13-01: no org slug inside the string.
	// It matters here beyond cosmetics — checks created through the service are
	// normalized, so a fixture planted directly into the DB with a legacy
	// spelling would silently stop matching them.
	parisRegion  = "@paris"
	londonRegion = "@london"
	cloudRegion  = "eu"
)

// createRegionBastion creates an SSH tunnel bastion allocated to the given
// regions. With a cloud region in the set it stores a server-decryptable
// envelope (not sealed-only) and needs no enrolled agent.
func createRegionBastion(t *testing.T, svc *checks.Service, ctx context.Context, //nolint:revive
	org *models.Organization, slug string, regionSlugs []string,
) checks.CheckResponse {
	t.Helper()

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: slug, Slug: slug, Type: "ssh", Regions: regionSlugs,
		Config: map[string]any{
			"host":                 "bastion.example.com",
			"expected_fingerprint": testFingerprint,
			"username":             "probe",
			"password":             "s3cret",
		},
	})
	require.NoError(t, err)

	return created
}

// TestTunnelRegionRulesOnCreate is the accept/reject matrix for the region
// rules (spec 2026-07-18-07, decisions 1–2): the dependent's private regions
// must be covered by the SSH check, and a cloud dependent needs a
// server-resolvable (non-sealed-only) SSH check.
func TestTunnelRegionRulesOnCreate(t *testing.T) {
	t.Parallel()

	svc, dbSvc, org := setupTunnelChecksService(t)
	ctx := t.Context()

	// A mixed-region bastion covering paris + london + a cloud region: not
	// sealed-only, so it can serve both private and cloud dependents.
	mixed := createRegionBastion(t, svc, ctx, org, "mixed", []string{parisRegion, londonRegion, cloudRegion})

	// A sealed-only bastion (config_sealed set, config_private NULL) planted
	// directly — that state is only reachable with an enrolled agent otherwise,
	// and the validation logic reads exactly these two columns.
	sealedOnly := createRegionBastion(t, svc, ctx, org, "sealedonly", []string{parisRegion, cloudRegion})
	plantSealedOnly(t, dbSvc, ctx, org, sealedOnly.UID, []string{parisRegion})

	tests := []struct {
		name       string
		slug       string
		regions    []string
		tunnelUID  string
		wantErr    string // "" = accept
		wantRegion string // substring the message must name
	}{
		{
			name: "private dependent covered", slug: "dep-a", regions: []string{parisRegion}, tunnelUID: mixed.UID,
		},
		{
			name: "cloud dependent, resolvable bastion", slug: "dep-b",
			regions: []string{cloudRegion}, tunnelUID: mixed.UID,
		},
		{
			name: "mixed dependent fully covered", slug: "dep-c",
			regions: []string{parisRegion, cloudRegion}, tunnelUID: mixed.UID,
		},
		{
			name: "private region not covered", slug: "dep-d", regions: []string{londonRegion}, tunnelUID: sealedOnly.UID,
			wantErr: "must be allocated to region", wantRegion: "@london",
		},
		{
			name: "cloud dependent, sealed-only bastion", slug: "dep-e",
			regions: []string{cloudRegion}, tunnelUID: sealedOnly.UID,
			wantErr: "sealed to private-region agents only",
		},
		{
			name: "private covered but cloud unresolvable", slug: "dep-f",
			regions: []string{parisRegion, cloudRegion}, tunnelUID: sealedOnly.UID,
			wantErr: "sealed to private-region agents only",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
				Name: tt.name, Slug: tt.slug, Type: "http", Regions: tt.regions,
				Config: map[string]any{
					"url":                              "http://internal.private/health",
					checkerdef.TunnelCheckUIDConfigKey: tt.tunnelUID,
				},
			})

			if tt.wantErr == "" {
				r.NoError(err)

				return
			}

			r.Error(err)
			r.Contains(err.Error(), tt.wantErr)

			if tt.wantRegion != "" {
				r.Contains(err.Error(), tt.wantRegion)
			}

			// Region rejections are field-level ConfigErrors on the tunnel key,
			// so the dashboard attaches them to the selector.
			configErr := checkerdef.IsConfigError(err)
			r.NotNil(configErr)
			r.Equal(checkerdef.TunnelCheckUIDConfigKey, configErr.Parameter)
		})
	}
}

// TestTunnelRegionRuleFiresOnRegionOnlyUpdate proves the region rule is
// enforced when the dependent's REGIONS change (config untouched) — the trigger
// that a config-less PATCH would otherwise slip past.
func TestTunnelRegionRuleFiresOnRegionOnlyUpdate(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createRegionBastion(t, svc, ctx, org, "bastion", []string{parisRegion, cloudRegion})

	dependent, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "api", Slug: "api", Type: "http", Regions: []string{parisRegion},
		Config: map[string]any{
			"url":                              "http://internal.private/health",
			checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
		},
	})
	r.NoError(err)

	// Moving the dependent to a region the bastion does not cover is rejected,
	// even though config is not part of the PATCH.
	_, err = svc.UpdateCheck(ctx, org.Slug, *dependent.Slug, &checks.UpdateCheckRequest{
		Regions: &[]string{londonRegion},
	})
	r.Error(err)
	r.Contains(err.Error(), "must be allocated to region")
	r.Contains(err.Error(), "@london")

	// Moving it to a region the bastion DOES cover is accepted.
	_, err = svc.UpdateCheck(ctx, org.Slug, *dependent.Slug, &checks.UpdateCheckRequest{
		Regions: &[]string{cloudRegion},
	})
	r.NoError(err)
}

// TestTunnelRegionNarrowingOnBastionRejected proves the SSH-check side guard:
// removing a private region an in-use bastion covers must not strand the
// dependent that runs there.
func TestTunnelRegionNarrowingOnBastionRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createRegionBastion(t, svc, ctx, org, "bastion",
		[]string{parisRegion, londonRegion, cloudRegion})

	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "london-api", Slug: "london-api", Type: "http", Regions: []string{londonRegion},
		Config: map[string]any{
			"url":                              "http://internal.private/health",
			checkerdef.TunnelCheckUIDConfigKey: bastion.UID,
		},
	})
	r.NoError(err)

	// Dropping london from the bastion strands london-api → 409 naming it.
	_, err = svc.UpdateCheck(ctx, org.Slug, *bastion.Slug, &checks.UpdateCheckRequest{
		Regions: &[]string{parisRegion, cloudRegion},
	})
	r.Error(err)
	r.ErrorIs(err, checks.ErrTunnelRegionNarrowed)
	r.Contains(err.Error(), "london-api")

	// Keeping london covered (only reordering / dropping an unused region) is
	// allowed.
	_, err = svc.UpdateCheck(ctx, org.Slug, *bastion.Slug, &checks.UpdateCheckRequest{
		Regions: &[]string{parisRegion, londonRegion},
	})
	r.NoError(err)
}

// plantSealedOnly rewrites an SSH check row into the sealed-only shape
// (config_sealed present, config_private cleared, private-only regions) — the
// state an SSH check reaches when it targets private regions exclusively.
func plantSealedOnly(t *testing.T, dbSvc db.Service, ctx context.Context, //nolint:revive
	org *models.Organization, checkUID string, privateRegions []string,
) {
	t.Helper()

	sealed := `{"v":2,"alg":"age-x25519","fingerprints":["deadbeef"],"ct":"AAAA"}`
	regionsCopy := append([]string(nil), privateRegions...)

	require.NoError(t, dbSvc.UpdateCheck(ctx, checkUID, &models.CheckUpdate{
		ConfigSealed:       &sealed,
		ClearConfigPrivate: true,
		Regions:            &regionsCopy,
	}))

	// Sanity: the row is now genuinely sealed-only.
	row, err := dbSvc.GetCheck(ctx, org.UID, checkUID)
	require.NoError(t, err)
	require.NotNil(t, row.ConfigSealed)
	require.Nil(t, row.ConfigPrivate)
}

// createBastionInOtherOrg creates a REAL, fully valid SSH check (fingerprint and
// all) inside a different organization and returns its UID. Only the org
// boundary makes it unusable — which is exactly what the test needs to prove.
func createBastionInOtherOrg(
	t *testing.T, svc *checks.Service, dbSvc db.Service, ctx context.Context, //nolint:revive
) string {
	t.Helper()

	otherOrg := models.NewOrganization("tunnel-other", "Other Org")
	require.NoError(t, dbSvc.CreateOrganization(ctx, otherOrg))

	created, err := svc.CreateCheck(ctx, otherOrg.Slug, checks.CreateCheckRequest{
		Name: "their-bastion", Slug: "their-bastion", Type: "ssh",
		Config: map[string]any{
			"host":                 "bastion.other.example.com",
			"expected_fingerprint": testFingerprint,
			"username":             "probe",
			"password":             "s3cret",
		},
	})
	require.NoError(t, err)

	return created.UID
}

// The tunnel selector needs "the org's ssh checks", which the list endpoint's
// `?type=` filter provides. Nothing else in the API could express it.
func TestListChecksFiltersByType(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	svc, _, org := setupTunnelChecksService(t)
	ctx := t.Context()

	bastion := createBastion(t, svc, ctx, org, "bastion")

	_, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "api", Slug: "api", Type: "http",
		Config: map[string]any{"url": "https://example.com"},
	})
	r.NoError(err)

	listed, err := svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{Types: []string{"ssh"}})
	r.NoError(err)
	r.Len(listed.Data, 1)
	r.Equal(bastion.UID, listed.Data[0].UID)

	// Multi-value, and no filter means every type.
	listed, err = svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{Types: []string{"ssh", "http"}})
	r.NoError(err)
	r.Len(listed.Data, 2)

	listed, err = svc.ListChecks(ctx, org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(listed.Data, 2)
}
