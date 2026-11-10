package checks

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

var errOtherForTest = errors.New("other")

func TestWriteSealingErrorCodes(t *testing.T) {
	t.Parallel()

	h := &Handler{svc: nil}
	h.HandlerBase = NewHandler(nil, &config.Config{}).HandlerBase

	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		handled    bool
	}{
		{
			name: "recipients changed",
			err: &SealingRecipientsChangedError{Recipients: []sealing.Recipient{
				{Kind: sealing.RecipientKindAgent, Region: "@dc1", Recipient: "age1x", Fingerprint: "aa"},
			}},
			wantStatus: http.StatusConflict, wantCode: "SEALING_RECIPIENTS_CHANGED", handled: true,
		},
		{
			name: "private regions", err: ErrSealedSecretsRequirePrivateRegions,
			wantStatus: http.StatusBadRequest, wantCode: "SEALED_SECRETS_REQUIRE_PRIVATE_REGIONS", handled: true,
		},
		{
			name: "must be sealed", err: ErrSecretsMustBeSealed,
			wantStatus: http.StatusBadRequest, wantCode: "SECRETS_MUST_BE_SEALED", handled: true,
		},
		{
			name: "invalid", err: invalidSealed("bad"),
			wantStatus: http.StatusBadRequest, wantCode: "VALIDATION_ERROR", handled: true,
		},
		{name: "other", err: errOtherForTest, handled: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)

			handled, err := h.writeSealingError(rec, req, tc.err)
			r.NoError(err)
			r.Equal(tc.handled, handled)

			if !tc.handled {
				return
			}

			r.Equal(tc.wantStatus, rec.Code)

			var body map[string]any
			r.NoError(json.Unmarshal(rec.Body.Bytes(), &body))
			r.Equal(tc.wantCode, body["code"])

			if tc.wantStatus == http.StatusConflict {
				r.Len(body["recipients"], 1)
			}
		})
	}
}
