package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// socketCheckReq is a tcp check on a Unix socket (spec 2026-11-24-01).
func socketCheckReq(path string, regions []string) checks.CreateCheckRequest {
	return checks.CreateCheckRequest{
		Type:    "tcp",
		Config:  map[string]any{"socket": path},
		Regions: regions,
	}
}

func hostPortCheckReq(regions []string) checks.CreateCheckRequest {
	return checks.CreateCheckRequest{
		Type:    "tcp",
		Config:  map[string]any{"host": "acme.com", "port": 443},
		Regions: regions,
	}
}

// TestCreateSocketCheck_SaaS: a socket target is host-local, so on SaaS it
// only saves pinned to private regions; the same check on host/port is fine
// on a shared region (positive control).
func TestCreateSocketCheck_SaaS(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSaaS)
	svc.SetLocalSocketAllowlist([]string{"/run/app/"})

	_, err := svc.CreateCheck(ctx, org.Slug, socketCheckReq("/run/app/a.sock", []string{"eu-west"}))
	r.ErrorContains(err, checks.ErrHostLocalNotAvailableInSaaS.Error())

	var cfgErr *checkerdef.ConfigError
	r.ErrorAs(err, &cfgErr)
	r.Equal("socket", cfgErr.Parameter)

	_, err = svc.CreateCheck(ctx, org.Slug, socketCheckReq("/run/app/a.sock", nil))
	r.ErrorContains(err, checks.ErrHostLocalNotAvailableInSaaS.Error(), "auto placement is shared")

	created, err := svc.CreateCheck(ctx, org.Slug, socketCheckReq("/run/app/a.sock", []string{"@office"}))
	r.NoError(err)
	r.Equal([]string{"@office"}, created.Regions)

	_, err = svc.CreateCheck(ctx, org.Slug, hostPortCheckReq([]string{"eu-west"}))
	r.NoError(err)

	// A region-only PATCH reads the stored config: moving the socket check
	// onto a shared region is refused.
	newRegions := []string{"eu-west"}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Regions: &newRegions})
	r.ErrorContains(err, checks.ErrHostLocalNotAvailableInSaaS.Error())
}

// TestCreateSocketCheck_SelfHostedAllowlist: the save-time allowlist rule.
func TestCreateSocketCheck_SelfHostedAllowlist(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)
	svc.SetLocalSocketAllowlist([]string{"/run/app/"})

	// Positive control: inside the allowlist, on a shared region.
	_, err := svc.CreateCheck(ctx, org.Slug, socketCheckReq("/run/app/a.sock", nil))
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, org.Slug, hostPortCheckReq(nil))
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, org.Slug, socketCheckReq("/run/other/a.sock", nil))
	r.ErrorContains(err, checkerdef.LocalSocketSetting)

	var cfgErr *checkerdef.ConfigError
	r.ErrorAs(err, &cfgErr)
	r.Equal("socket", cfgErr.Parameter)

	// Pinned to private regions only, the agent's allowlist decides.
	_, err = svc.CreateCheck(ctx, org.Slug, socketCheckReq("/run/other/a.sock", []string{"@office"}))
	r.NoError(err)

	// The syntax rule applies everywhere, private regions included.
	_, err = svc.CreateCheck(ctx, org.Slug, socketCheckReq("/run/app/../x.sock", []string{"@office"}))
	r.ErrorContains(err, "clean path")

	// The validate endpoint returns the same finding as create.
	resp, err := svc.ValidateCheck(ctx, org.Slug, &checks.ValidateCheckRequest{
		Type: "tcp", Config: map[string]any{"socket": "/run/other/a.sock"},
	})
	r.NoError(err)
	r.False(resp.Valid)
	r.NotEmpty(resp.Fields)

	found := false

	for _, field := range resp.Fields {
		if field.Name == "socket" {
			r.Contains(field.Message, checkerdef.LocalSocketSetting)

			found = true
		}
	}

	r.True(found, "fields: %+v", resp.Fields)

	resp, err = svc.ValidateCheck(ctx, org.Slug, &checks.ValidateCheckRequest{
		Type: "tcp", Config: map[string]any{"socket": "/run/app/a.sock"},
	})
	r.NoError(err)
	r.True(resp.Valid, "fields: %+v", resp.Fields)
}

// TestCreateSocketCheck_EmptyAllowlistRefusesEveryPath: the default is off.
func TestCreateSocketCheck_EmptyAllowlistRefusesEveryPath(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSelfHosted)

	for _, path := range []string{"/run/app/a.sock", "/var/run/docker.sock", "/tmp/x.sock"} {
		_, err := svc.CreateCheck(ctx, org.Slug, socketCheckReq(path, nil))
		r.ErrorContains(err, checkerdef.LocalSocketSetting, path)
	}
}

// TestCreateHTTPSocketCheck_SaaS: an http check with unix_socket is host-local
// like the tcp one; the same check without it is accepted (positive control).
func TestCreateHTTPSocketCheck_SaaS(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSaaS)
	svc.SetLocalSocketAllowlist([]string{"/run/app/"})

	req := func(extra map[string]any, regions []string) checks.CreateCheckRequest {
		cfg := map[string]any{"url": "http://localhost/health"}
		for key, value := range extra {
			cfg[key] = value
		}

		return checks.CreateCheckRequest{Type: "http", Config: cfg, Regions: regions}
	}

	_, err := svc.CreateCheck(ctx, org.Slug, req(map[string]any{"unix_socket": "/run/app/a.sock"}, []string{"eu-west"}))
	r.ErrorContains(err, checks.ErrHostLocalNotAvailableInSaaS.Error())

	var cfgErr *checkerdef.ConfigError
	r.ErrorAs(err, &cfgErr)
	r.Equal("unix_socket", cfgErr.Parameter)

	_, err = svc.CreateCheck(ctx, org.Slug, req(nil, []string{"eu-west"}))
	r.NoError(err)

	_, err = svc.CreateCheck(ctx, org.Slug, req(map[string]any{"unix_socket": "/run/app/a.sock"}, []string{"@office"}))
	r.NoError(err)
}

// TestCreateSystemdCheck_SaaS: a systemd check is host-local by type (issue
// #1000): on SaaS it is rejected on a shared region and saved on a private one.
func TestCreateSystemdCheck_SaaS(t *testing.T) {
	t.Parallel()
	r := require.New(t)
	ctx := t.Context()

	svc, org := setupDockerGateService(t, config.DeploymentModeSaaS)

	req := func(regions []string) checks.CreateCheckRequest {
		return checks.CreateCheckRequest{
			Type:    "systemd",
			Config:  map[string]any{"unit": "nginx"},
			Regions: regions,
		}
	}

	_, err := svc.CreateCheck(ctx, org.Slug, req([]string{"eu-west"}))
	r.ErrorContains(err, checks.ErrSystemdNotAvailableInSaaS.Error())

	var cfgErr *checkerdef.ConfigError
	r.ErrorAs(err, &cfgErr)
	r.Equal("unit", cfgErr.Parameter)

	_, err = svc.CreateCheck(ctx, org.Slug, req(nil))
	r.ErrorContains(err, checks.ErrSystemdNotAvailableInSaaS.Error(), "auto placement is shared")

	created, err := svc.CreateCheck(ctx, org.Slug, req([]string{"@office"}))
	r.NoError(err)
	r.Equal([]string{"@office"}, created.Regions)
	r.Equal("systemd-nginx-service", *created.Slug)
}
