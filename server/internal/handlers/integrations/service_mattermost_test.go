package integrations

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestValidateMattermostMessageFormat(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		settings models.JSONMap
		wantErr  bool
	}{
		{"missing defaults to attachments", models.JSONMap{}, false},
		{"empty", models.JSONMap{"message_format": ""}, false},
		{"attachments", models.JSONMap{"message_format": "attachments"}, false},
		{"blocks", models.JSONMap{"message_format": "mm_blocks"}, false},
		{"unknown", models.JSONMap{"message_format": "slack_blocks"}, true},
		{"wrong type", models.JSONMap{"message_format": true}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateMattermostSettings(tc.settings)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrInvalidSettings)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidateConnectionTypeMattermostFormat(t *testing.T) {
	t.Parallel()

	require.NoError(t, validateConnectionType(
		models.ConnectionTypeMattermost, models.JSONMap{"message_format": "mm_blocks"}))
	require.ErrorIs(t, validateConnectionType(
		models.ConnectionTypeMattermost, models.JSONMap{"message_format": "nope"}), ErrInvalidSettings)
}
