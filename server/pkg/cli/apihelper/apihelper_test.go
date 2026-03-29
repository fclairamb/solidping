package apihelper

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/pkg/cli/config"
)

const testJWTToken = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3MDQwNjcyMDAsInN1YiI6InRlc3QifQ.sig"

func TestParseJWTExpiration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		token       string
		expectError bool
	}{
		{
			name:        "valid JWT token",
			token:       testJWTToken,
			expectError: false,
		},
		{
			name:        "invalid JWT format",
			token:       "not-a-jwt-token",
			expectError: true,
		},
		{
			name:        "empty token",
			token:       "",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			exp, err := parseJWTExpiration(tt.token)
			if tt.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotZero(t, exp)
			}
		})
	}
}

func TestTokenDataValidation(t *testing.T) {
	t.Parallel()

	now := time.Now()
	future := now.Add(1 * time.Hour)
	past := now.Add(-1 * time.Hour)

	tests := []struct {
		name               string
		tokenData          *TokenData
		expectAccessValid  bool
		expectRefreshValid bool
	}{
		{
			name: "both tokens valid",
			tokenData: &TokenData{
				AccessToken:           "access",
				AccessTokenExpiresAt:  future,
				RefreshToken:          "refresh",
				RefreshTokenExpiresAt: future,
			},
			expectAccessValid:  true,
			expectRefreshValid: true,
		},
		{
			name: "access expired, refresh valid",
			tokenData: &TokenData{
				AccessToken:           "access",
				AccessTokenExpiresAt:  past,
				RefreshToken:          "refresh",
				RefreshTokenExpiresAt: future,
			},
			expectAccessValid:  false,
			expectRefreshValid: true,
		},
		{
			name: "both expired",
			tokenData: &TokenData{
				AccessToken:           "access",
				AccessTokenExpiresAt:  past,
				RefreshToken:          "refresh",
				RefreshTokenExpiresAt: past,
			},
			expectAccessValid:  false,
			expectRefreshValid: false,
		},
		{
			name: "no refresh token",
			tokenData: &TokenData{
				AccessToken:           "access",
				AccessTokenExpiresAt:  future,
				RefreshToken:          "",
				RefreshTokenExpiresAt: time.Time{},
			},
			expectAccessValid:  true,
			expectRefreshValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expectAccessValid, tt.tokenData.IsAccessTokenValid())
			assert.Equal(t, tt.expectRefreshValid, tt.tokenData.IsRefreshTokenValid())
		})
	}
}

func TestTokenFileIO(t *testing.T) { //nolint:tparallel // Subtests share state and cannot run in parallel
	t.Parallel()

	// Create temporary directory for test
	tmpDir, err := os.MkdirTemp("", "apihelper-test-*")
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = os.RemoveAll(tmpDir)
	})

	tokenPath := filepath.Join(tmpDir, "token")
	cfg := &config.Config{
		URL: "http://localhost:4000",
		Org: "test",
	}

	helper := NewHelper(cfg, tokenPath, false)

	t.Run("save and read JSON format", func(t *testing.T) { //nolint:paralleltest // Shares state with other subtests
		tokenData := &TokenData{
			AccessToken:           "test-access-token",
			AccessTokenExpiresAt:  time.Now().Add(1 * time.Hour),
			RefreshToken:          "test-refresh-token",
			RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour),
		}

		// Save token data
		err := helper.saveTokenFile(tokenData)
		require.NoError(t, err)

		// Read token data back
		readData, err := helper.readTokenFile()
		require.NoError(t, err)
		require.NotNil(t, readData)

		assert.Equal(t, tokenData.AccessToken, readData.AccessToken)
		assert.Equal(t, tokenData.RefreshToken, readData.RefreshToken)
		assert.WithinDuration(t, tokenData.AccessTokenExpiresAt, readData.AccessTokenExpiresAt, 1*time.Second)
		assert.WithinDuration(t, tokenData.RefreshTokenExpiresAt, readData.RefreshTokenExpiresAt, 1*time.Second)
	})

	t.Run("read invalid JSON format", func(t *testing.T) { //nolint:paralleltest // Shares state with other subtests
		// Create an invalid JSON file
		err := os.WriteFile(tokenPath, []byte("not valid json"), 0o600)
		require.NoError(t, err)

		// Read should fail
		readData, err := helper.readTokenFile()
		require.Error(t, err)
		assert.Nil(t, readData)
		assert.Contains(t, err.Error(), "failed to parse token file")
	})

	t.Run("read non-existent file", func(t *testing.T) { //nolint:paralleltest // Shares state with other subtests
		// Remove the file
		_ = os.Remove(tokenPath)

		readData, err := helper.readTokenFile()
		require.NoError(t, err)
		assert.Nil(t, readData)
	})

	t.Run("file permissions", func(t *testing.T) { //nolint:paralleltest // Shares state with other subtests
		tokenData := &TokenData{
			AccessToken:           "test-access-token",
			AccessTokenExpiresAt:  time.Now().Add(1 * time.Hour),
			RefreshToken:          "test-refresh-token",
			RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour),
		}

		err := helper.saveTokenFile(tokenData)
		require.NoError(t, err)

		// Check file permissions
		info, err := os.Stat(tokenPath)
		require.NoError(t, err)
		assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	})
}

func TestCreateTokenData(t *testing.T) {
	t.Parallel()

	t.Run("valid tokens", func(t *testing.T) {
		t.Parallel()

		accessToken := testJWTToken
		refreshToken := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJleHAiOjE3MDQxNTM2MDAsInN1YiI6InRlc3QifQ.sig"

		tokenData, err := createTokenData(accessToken, refreshToken)
		require.NoError(t, err)
		require.NotNil(t, tokenData)

		assert.Equal(t, accessToken, tokenData.AccessToken)
		assert.Equal(t, refreshToken, tokenData.RefreshToken)
		assert.NotZero(t, tokenData.AccessTokenExpiresAt)
		assert.NotZero(t, tokenData.RefreshTokenExpiresAt)
	})

	t.Run("invalid access token", func(t *testing.T) {
		t.Parallel()

		_, err := createTokenData("invalid-token", "")
		require.Error(t, err)
	})

	t.Run("no refresh token", func(t *testing.T) {
		t.Parallel()

		accessToken := testJWTToken

		tokenData, err := createTokenData(accessToken, "")
		require.NoError(t, err)
		require.NotNil(t, tokenData)

		assert.Equal(t, accessToken, tokenData.AccessToken)
		assert.Empty(t, tokenData.RefreshToken)
		assert.Zero(t, tokenData.RefreshTokenExpiresAt)
	})
}
