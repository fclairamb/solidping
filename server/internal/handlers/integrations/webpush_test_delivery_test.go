package integrations_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	webpushgo "github.com/SherClockHolmes/webpush-go"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/webpush"
)

func webPushSub(endpoint string) map[string]any {
	return map[string]any{
		"endpoint": endpoint,
		"keys": map[string]any{
			"p256dh": "BLiMDpdL9AFok4VWdDMMek7hFVBaleGPi8DVegkLJFH7IFnJo5zAP1GjH50H-njZFrZJ1etQf3F38z68FzSPa1Y",
			"auth":   "tBHItnDR9oNFbwKMkhY7lQ",
		},
	}
}

func TestTestIntegration_WebPush(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	ctx := t.Context()

	priv, pub, err := webpushgo.GenerateVAPIDKeys()
	r.NoError(err)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	org := models.NewOrganization("webpush-org", "Webpush Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer good.Close()

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
	}))
	defer bad.Close()

	create := func(svc *integrations.Service, name string, subs any) string {
		settings := map[string]any{}
		if subs != nil {
			settings["subscriptions"] = subs
		}

		created, createErr := svc.CreateIntegration(ctx, org.Slug, integrations.CreateIntegrationRequest{
			Type: "webpush", Name: name, Settings: settings,
		})
		r.NoError(createErr)

		return created.UID
	}

	// Not configured: the test reports it instead of sending.
	unconfigured := integrations.NewService(dbSvc, creds, nil, &config.Config{})
	_, err = unconfigured.TestIntegration(ctx, org.Slug, create(unconfigured, "unconfigured", nil))
	r.Error(err)

	reg := &services.Registry{WebPushOptions: webpush.Options{
		VAPIDPublicKey: pub, VAPIDPrivateKey: priv, Subject: "mailto:test@acme.com",
	}}
	svc := integrations.NewService(dbSvc, creds, reg, &config.Config{})

	res, err := svc.TestIntegration(ctx, org.Slug, create(svc, "empty", nil))
	r.NoError(err)
	r.False(res.Success)
	r.Contains(res.Error, "no subscriptions")

	res, err = svc.TestIntegration(ctx, org.Slug, create(svc, "ok", []any{webPushSub(good.URL)}))
	r.NoError(err)
	r.True(res.Success)
	r.Empty(res.Error)

	res, err = svc.TestIntegration(ctx, org.Slug, create(svc, "allfail", []any{webPushSub(bad.URL)}))
	r.NoError(err)
	r.False(res.Success)
	r.NotEmpty(res.Error)

	res, err = svc.TestIntegration(ctx, org.Slug,
		create(svc, "partial", []any{webPushSub(good.URL), webPushSub(bad.URL)}))
	r.NoError(err)
	r.True(res.Success)
	r.Contains(res.Error, "1/2 delivered")
}
