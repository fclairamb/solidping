package models_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestSMSFactorSettingsRoundTrip(t *testing.T) {
	t.Parallel()

	in := &models.SMSFactorSettings{Token: "tok", Sender: "ACME", ToNumbers: []string{"+33612345678"}}

	asMap, err := in.ToJSONMap()
	require.NoError(t, err)
	require.Equal(t, "ACME", asMap["sender"])

	out, err := models.SMSFactorSettingsFromJSONMap(asMap)
	require.NoError(t, err)
	require.Equal(t, in, out)
}

func TestSMSFactorSettingsFromJSONMapErrors(t *testing.T) {
	t.Parallel()

	_, err := models.SMSFactorSettingsFromJSONMap(models.JSONMap{"token": make(chan int)})
	require.Error(t, err)

	_, err = models.SMSFactorSettingsFromJSONMap(models.JSONMap{"to_numbers": "not-a-list"})
	require.Error(t, err)
}
