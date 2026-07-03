package passwords

import (
	"fmt"
	"sync"

	"github.com/fclairamb/solidping/server/internal/config"
)

// Argon2Params are the resolved argon2id cost parameters.
type Argon2Params struct {
	Memory     uint32 // KiB
	Time       uint32
	Threads    uint8
	KeyLength  uint32
	SaltLength uint32
}

// BcryptParams are the resolved bcrypt cost parameters.
type BcryptParams struct {
	Cost int
}

// Policy is the resolved hashing policy: the active algorithm plus the cost
// parameters used when minting a new hash.
type Policy struct {
	Algorithm string
	Argon2    Argon2Params
	Bcrypt    BcryptParams
	// RehashOnLogin gates the lazy login-time rehash (see ShouldRehash). It is
	// part of the resolved policy so it hot-swaps with the rest of it.
	RehashOnLogin bool
}

// defaultBcryptCost is the fallback bcrypt cost when none is configured.
const defaultBcryptCost = 12

// defaultArgon2Params reproduces the historically hardcoded argon2id profile
// (m=64 MiB, t=3, p=4, 32-byte key, 16-byte salt). Used as the fallback when no
// policy has been set so package-level Hash keeps working out of the box.
//
//nolint:gochecknoglobals // immutable default profile
var defaultArgon2Params = Argon2Params{
	Memory:     64 * 1024,
	Time:       3,
	Threads:    4,
	KeyLength:  32,
	SaltLength: 16,
}

// defaultPolicy is the process-wide policy consulted by Hash and NeedsRehash.
// It defaults to the legacy argon2id profile so the package is usable before
// SetDefaultPolicy is called (and in tests). Verify never reads it — it
// dispatches purely on the stored hash marker, keeping it safe under
// t.Parallel().
//
//nolint:gochecknoglobals // process-wide hashing policy, guarded by policyMu
var (
	policyMu      sync.RWMutex
	defaultPolicy = Policy{
		Algorithm:     argon2idID,
		Argon2:        defaultArgon2Params,
		Bcrypt:        BcryptParams{Cost: defaultBcryptCost},
		RehashOnLogin: true,
	}
)

// SetDefaultPolicy installs p as the process-wide policy used by Hash and
// NeedsRehash. Call once at startup after resolving it from config.
func SetDefaultPolicy(p Policy) {
	policyMu.Lock()
	defer policyMu.Unlock()
	defaultPolicy = p
}

// getDefaultPolicy returns a copy of the active policy.
func getDefaultPolicy() Policy {
	policyMu.RLock()
	defer policyMu.RUnlock()
	return defaultPolicy
}

// PolicyFromConfig maps the password-hashing config block onto a resolved
// Policy. Validation (cost floors etc.) is performed by config.Config.Validate
// at load time.
//
// A zero-value / unset block resolves to the legacy argon2id default profile, so
// a Config assembled directly (e.g. in tests) without going through config.Load
// behaves exactly like the documented default. A genuinely *unknown* (non-empty
// but unsupported) algorithm is rejected — never a silent fallback.
func PolicyFromConfig(authCfg *config.AuthConfig) (Policy, error) {
	pwCfg := authCfg.Password

	algorithm := pwCfg.Algorithm
	if algorithm == "" {
		algorithm = argon2idID
	}

	policy := Policy{
		Algorithm:     algorithm,
		Argon2:        resolveArgon2Params(pwCfg.Argon2),
		Bcrypt:        BcryptParams{Cost: pwCfg.Bcrypt.Cost},
		RehashOnLogin: pwCfg.RehashOnLogin,
	}
	if policy.Bcrypt.Cost == 0 {
		policy.Bcrypt.Cost = defaultBcryptCost
	}
	// A zero-value config block (e.g. a Config assembled directly in tests
	// without going through config.Load) resolves to the documented default,
	// which keeps rehash-on-login enabled. config.Load itself defaults the
	// field to true, so this only affects hand-built configs.
	if isZeroValuePasswordConfig(pwCfg) {
		policy.RehashOnLogin = true
	}

	switch policy.Algorithm {
	case argon2idID, bcryptID:
		return policy, nil
	default:
		return Policy{}, fmt.Errorf("%w: %q", ErrUnknownAlgorithm, policy.Algorithm)
	}
}

// isZeroValuePasswordConfig reports whether the whole password config block is
// the Go zero value, i.e. it was never populated (not even by config.Load's
// defaults). Used to decide whether RehashOnLogin should default to true.
func isZeroValuePasswordConfig(pwCfg config.PasswordConfig) bool {
	return pwCfg == config.PasswordConfig{}
}

// ShouldRehash reports whether a stored hash should be re-minted on the user's
// next successful login. It is the single gate consulted by the login rehash
// hook: rehash only happens when the active policy has RehashOnLogin enabled AND
// the stored hash no longer matches the policy (algorithm or cost drifted).
func ShouldRehash(hash string) bool {
	return getDefaultPolicy().RehashOnLogin && NeedsRehash(hash)
}

// resolveArgon2Params maps config argon2 params onto the policy struct, filling
// any zero-value field with the legacy default so a partially-specified or
// zero-value block is still usable.
func resolveArgon2Params(cfg config.Argon2Params) Argon2Params {
	params := Argon2Params{
		Memory:     cfg.Memory,
		Time:       cfg.Time,
		Threads:    cfg.Threads,
		KeyLength:  cfg.KeyLength,
		SaltLength: cfg.SaltLength,
	}
	if params.Memory == 0 {
		params.Memory = defaultArgon2Params.Memory
	}
	if params.Time == 0 {
		params.Time = defaultArgon2Params.Time
	}
	if params.Threads == 0 {
		params.Threads = defaultArgon2Params.Threads
	}
	if params.KeyLength == 0 {
		params.KeyLength = defaultArgon2Params.KeyLength
	}
	if params.SaltLength == 0 {
		params.SaltLength = defaultArgon2Params.SaltLength
	}

	return params
}
