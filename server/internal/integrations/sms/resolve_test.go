package sms_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	smssvc "github.com/fclairamb/solidping/server/internal/integrations/sms"
)

// fakeSender is a Sender (and VoiceCaller) that records nothing: the resolver
// tests only care about which one was picked.
type fakeSender struct{ name string }

func (f *fakeSender) SendSMS(context.Context, *smssvc.SendParams) (*smssvc.SendResult, error) {
	return &smssvc.SendResult{ProviderMessageID: f.name}, nil
}

func (f *fakeSender) PlaceCall(context.Context, *smssvc.CallParams) (*smssvc.SendResult, error) {
	return &smssvc.SendResult{ProviderMessageID: f.name}, nil
}

func newResolverDB(t *testing.T) (db.Service, string) {
	t.Helper()

	ctx := t.Context()
	r := require.New(t)

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("resolver-org", "Resolver Org")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	return dbSvc, org.UID
}

func addConnection(
	t *testing.T, dbSvc db.Service, orgUID string, connType models.ConnectionType, enabled bool, settings models.JSONMap,
) {
	t.Helper()

	conn := models.NewIntegration(orgUID, connType, string(connType))
	conn.Enabled = enabled
	conn.IsDefault = true
	conn.Settings = settings
	require.NoError(t, dbSvc.CreateChannel(t.Context(), conn))
}

func smsFactorSettings() models.JSONMap {
	return models.JSONMap{"token": "sf-token", "sender": "ACME"}
}

func twilioSettings() models.JSONMap {
	return models.JSONMap{
		"account_sid": "AC00000000000000000000000000000001",
		"auth_token":  "tok",
		"from_number": "+15559990000",
	}
}

func TestResolve_SMSFactorBringYourOwn(t *testing.T) {
	t.Parallel()

	instanceSMS := &fakeSender{name: "instance-sms"}
	instanceVoice := &fakeSender{name: "instance-voice"}
	byoSMSFactor := &fakeSender{name: "byo-smsfactor"}
	byoTwilio := &fakeSender{name: "byo-twilio"}

	tests := []struct {
		name          string
		setup         func(t *testing.T, dbSvc db.Service, orgUID string)
		withInstance  bool
		wantSMS       string
		wantSMSMode   smssvc.Mode
		wantVoice     string
		wantVoiceMode smssvc.Mode
		wantSenderID  string
		wantConn      bool
	}{
		{
			name: "smsfactor only, no instance provider",
			setup: func(t *testing.T, dbSvc db.Service, orgUID string) {
				t.Helper()
				addConnection(t, dbSvc, orgUID, models.ConnectionTypeSMSFactor, true, smsFactorSettings())
			},
			wantSMS: "byo-smsfactor", wantSMSMode: smssvc.ModeBringYourOwn,
			wantVoiceMode: smssvc.ModeUnavailable, wantSenderID: "ACME",
		},
		{
			name: "smsfactor beats the instance for SMS, voice stays on the instance",
			setup: func(t *testing.T, dbSvc db.Service, orgUID string) {
				t.Helper()
				addConnection(t, dbSvc, orgUID, models.ConnectionTypeSMSFactor, true, smsFactorSettings())
			},
			withInstance: true,
			wantSMS:      "byo-smsfactor", wantSMSMode: smssvc.ModeBringYourOwn,
			wantVoice: "instance-voice", wantVoiceMode: smssvc.ModeServer, wantSenderID: "ACME",
		},
		{
			name: "a disabled smsfactor connection is ignored",
			setup: func(t *testing.T, dbSvc db.Service, orgUID string) {
				t.Helper()
				addConnection(t, dbSvc, orgUID, models.ConnectionTypeSMSFactor, false, smsFactorSettings())
			},
			withInstance: true,
			wantSMS:      "instance-sms", wantSMSMode: smssvc.ModeServer,
			wantVoice: "instance-voice", wantVoiceMode: smssvc.ModeServer, wantSenderID: "+15550001111",
		},
		{
			name: "twilio wins over smsfactor",
			setup: func(t *testing.T, dbSvc db.Service, orgUID string) {
				t.Helper()
				addConnection(t, dbSvc, orgUID, models.ConnectionTypeSMSFactor, true, smsFactorSettings())
				addConnection(t, dbSvc, orgUID, models.ConnectionTypeTwilio, true, twilioSettings())
			},
			withInstance: true,
			wantSMS:      "byo-twilio", wantSMSMode: smssvc.ModeBringYourOwn,
			wantVoice: "instance-voice", wantVoiceMode: smssvc.ModeServer, wantSenderID: "+15559990000",
			wantConn: true,
		},
		{
			name:  "no connection at all",
			setup: func(*testing.T, db.Service, string) {},
			// Positive control for the instance fallback.
			withInstance: true,
			wantSMS:      "instance-sms", wantSMSMode: smssvc.ModeServer,
			wantVoice: "instance-voice", wantVoiceMode: smssvc.ModeServer, wantSenderID: "+15550001111",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			dbSvc, orgUID := newResolverDB(t)
			tt.setup(t, dbSvc, orgUID)

			var (
				instSMS   smssvc.Sender
				instVoice smssvc.VoiceCaller
			)

			if tt.withInstance {
				instSMS, instVoice = instanceSMS, instanceVoice
			}

			var gotSettings *models.SMSFactorSettings

			resolver := smssvc.NewResolver(dbSvc, nil, instSMS, instVoice, "+15550001111",
				smssvc.WithOrgSenderFactory(func(*models.TwilioSettings) smssvc.PhoneSender { return byoTwilio }),
				smssvc.WithOrgSMSFactorSenderFactory(func(s *models.SMSFactorSettings) smssvc.Sender {
					gotSettings = s

					return byoSMSFactor
				}),
			)

			res, err := resolver.Resolve(t.Context(), orgUID)
			r.NoError(err)

			r.Equal(tt.wantSMSMode, res.SMSMode)
			r.Equal(tt.wantVoiceMode, res.VoiceMode)
			r.Equal(tt.wantSenderID, res.SenderID)
			r.Equal(tt.wantConn, res.Conn != nil, "Conn is only ever the Twilio integration")

			sent, err := res.Sender.SendSMS(t.Context(), &smssvc.SendParams{})
			r.NoError(err)
			r.Equal(tt.wantSMS, sent.ProviderMessageID)

			if tt.wantVoice == "" {
				r.Nil(res.Voice)
			} else {
				placed, err := res.Voice.PlaceCall(t.Context(), &smssvc.CallParams{})
				r.NoError(err)
				r.Equal(tt.wantVoice, placed.ProviderMessageID)
			}

			if tt.wantSMS == "byo-smsfactor" {
				r.NotNil(gotSettings)
				r.Equal("sf-token", gotSettings.Token, "the factory gets the decrypted settings")
				r.False(res.InstanceCredentialsForSMS(), "a bring-your-own send never consumes instance guards")
			}
		})
	}
}
