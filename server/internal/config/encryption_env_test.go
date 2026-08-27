package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestApplyEncryptionEnv covers the variable whose absence made the credentials
// service store secrets in plaintext while its own remediation message told the
// operator to set it. koanf collapses every underscore after SP_ to a dot, so
// SP_ENCRYPTION_MASTER_KEY arrived as "encryption.master.key" and never reached
// the "master_key" tag.
func TestApplyEncryptionEnv(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_ENCRYPTION_MASTER_KEY", "dGVzdC1rZXktbm90LWEtcmVhbC1vbmUtMzJiCg==")
	t.Setenv("SP_ENCRYPTION_MASTER_KEY_FILE", "/run/secrets/kek")
	t.Setenv("SP_ENCRYPTION_AUTO_MIGRATE", "false")

	cfg := EncryptionConfig{AutoMigrate: true}
	applyEncryptionEnv(&cfg)

	r.Equal("dGVzdC1rZXktbm90LWEtcmVhbC1vbmUtMzJiCg==", cfg.MasterKey)
	r.Equal("/run/secrets/kek", cfg.MasterKeyFile)
	r.False(cfg.AutoMigrate)
}

// TestApplyEncryptionEnv_UnsetKeepsDefaults guards the direction that actually
// bit us: an operator who sets nothing must keep the shipped defaults, and in
// particular AutoMigrate must stay true so supplying a key later still sweeps
// the plaintext rows.
func TestApplyEncryptionEnv_UnsetKeepsDefaults(t *testing.T) {
	r := require.New(t)

	// Pinned empty rather than trusting the ambient environment: "" is exactly
	// what the helper treats as unset, so this states the case under test
	// instead of inheriting whatever the shell happened to export.
	t.Setenv("SP_ENCRYPTION_MASTER_KEY", "")
	t.Setenv("SP_ENCRYPTION_MASTER_KEY_FILE", "")
	t.Setenv("SP_ENCRYPTION_AUTO_MIGRATE", "")

	cfg := EncryptionConfig{AutoMigrate: true}
	applyEncryptionEnv(&cfg)

	r.Empty(cfg.MasterKey)
	r.Empty(cfg.MasterKeyFile)
	r.True(cfg.AutoMigrate)
}

// TestLoadBindsEncryptionMasterKey is the regression test proper: it goes
// through Load, the path production actually takes, rather than calling the
// helper directly. Before applyEncryptionEnv existed this returned "".
func TestLoadBindsEncryptionMasterKey(t *testing.T) {
	r := require.New(t)

	t.Setenv("SP_ENCRYPTION_MASTER_KEY", "dGVzdC1rZXktbm90LWEtcmVhbC1vbmUtMzJiCg==")

	cfg, err := Load()
	r.NoError(err)
	r.Equal("dGVzdC1rZXktbm90LWEtcmVhbC1vbmUtMzJiCg==", cfg.Encryption.MasterKey)
}
