package freebox_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/integrations/freebox"
)

func TestStartPairing(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/login/authorize/", func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var body freebox.AuthorizeRequest

		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, freebox.DefaultAppID, body.AppID)
		require.Equal(t, "Floor1-Box", body.DeviceName)

		raw, _ := json.Marshal(freebox.AuthorizeResult{AppToken: "token-x", TrackID: 11})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := require.New(t)

	res, err := freebox.StartPairing(context.Background(), &models.FreeboxSettings{
		BaseURL:    srv.URL,
		AppID:      freebox.DefaultAppID,
		DeviceName: "Floor1-Box",
	})
	r.NoError(err)
	r.Equal("token-x", res.AppToken)
	r.Equal(11, res.TrackID)
}

func TestCheckPairingStatusMapsTerminalErrors(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status        string
		expectedErr   error
		expectedValue string
	}{
		{freebox.StatusGranted, nil, freebox.StatusGranted},
		{freebox.StatusPending, nil, freebox.StatusPending},
		{freebox.StatusUnknown, nil, freebox.StatusUnknown},
		{freebox.StatusDenied, freebox.ErrPairingDenied, freebox.StatusDenied},
		{freebox.StatusTimeout, freebox.ErrPairingTimeout, freebox.StatusTimeout},
	}

	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			t.Parallel()

			mux := http.NewServeMux()
			mux.HandleFunc("/api/v4/login/authorize/", func(w http.ResponseWriter, _ *http.Request) {
				raw, _ := json.Marshal(freebox.PairingStatus{Status: tc.status})
				_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw})
			})

			srv := httptest.NewServer(mux)
			defer srv.Close()

			r := require.New(t)

			got, err := freebox.CheckPairingStatus(context.Background(), &models.FreeboxSettings{
				BaseURL: srv.URL,
			}, 1)

			if tc.expectedErr != nil {
				r.ErrorIs(err, tc.expectedErr)
			} else {
				r.NoError(err)
			}

			r.Equal(tc.expectedValue, got)
		})
	}
}

func TestValidateConnectionWithoutTokenFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	err := freebox.ValidateConnection(context.Background(),
		&models.FreeboxSettings{BaseURL: "http://nowhere"}, nil)
	r.ErrorIs(err, freebox.ErrAuthRequired)
}

func TestValidateConnectionOpensSession(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/login/", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(freebox.LoginChallenge{Challenge: "ch"})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw})
	})
	mux.HandleFunc("/api/v4/login/session/", func(w http.ResponseWriter, _ *http.Request) {
		raw, _ := json.Marshal(freebox.SessionResult{SessionToken: "ok"})
		_ = json.NewEncoder(w).Encode(freebox.APIResponse{Success: true, Result: raw})
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := require.New(t)

	err := freebox.ValidateConnection(
		context.Background(),
		&models.FreeboxSettings{BaseURL: srv.URL, AppID: freebox.DefaultAppID},
		&models.FreeboxPrivateSettings{AppToken: "ttt"},
	)
	r.NoError(err)
}
