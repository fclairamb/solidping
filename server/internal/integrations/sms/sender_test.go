package sms_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
)

func TestNewInstanceSender_SMSFactor(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var (
		mu     sync.Mutex
		bodies []string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)

		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()

		_, _ = w.Write([]byte(`{"status":1,"message":"OK","ticket":"777","sent":1}`))
	}))
	t.Cleanup(srv.Close)

	cfg := &config.SMSConfig{
		Enabled:   true,
		Provider:  config.SMSProviderSMSFactor,
		Sender:    "ACME",
		SMSFactor: config.SMSSMSFactorConfig{Token: "tok", BaseURL: srv.URL},
	}
	r.True(cfg.Active())

	sender, err := smssvc.NewInstanceSender(t.Context(), cfg)
	r.NoError(err)
	r.NotNil(sender)
	r.False(smssvc.IsOVHSender(sender))

	res, err := sender.SendSMS(t.Context(), &smssvc.SendParams{
		To: "+33612345678", Body: "hello", StatusCallback: "https://ignored.example.com",
	})
	r.NoError(err)
	r.Equal("777", res.ProviderMessageID)

	mu.Lock()
	defer mu.Unlock()
	r.Len(bodies, 1)
	r.Contains(bodies[0], `"value":"33612345678"`)
	r.Contains(bodies[0], `"sender":"ACME"`)
	r.NotContains(bodies[0], "ignored.example.com", "SMS Factor carries no per-message callback")
}

func TestNewInstanceSender_SMSFactorInvalidSender(t *testing.T) {
	t.Parallel()

	cfg := &config.SMSConfig{
		Enabled:   true,
		Provider:  config.SMSProviderSMSFactor,
		Sender:    "+33612345678",
		SMSFactor: config.SMSSMSFactorConfig{Token: "tok"},
	}

	_, err := smssvc.NewInstanceSender(t.Context(), cfg)
	require.ErrorIs(t, err, smssvc.ErrUnknownProvider)
}
