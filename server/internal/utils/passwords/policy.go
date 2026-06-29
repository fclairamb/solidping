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
		Algorithm: argon2idID,
		Argon2:    defaultArgon2Params,
		Bcrypt:    BcryptParams{Cost: defaultBcryptCost},
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
		Algorithm: algorithm,
		Argon2:    resolveArgon2Params(pwCfg.Argon2),
		Bcrypt:    BcryptParams{Cost: pwCfg.Bcrypt.Cost},
	}
	if policy.Bcrypt.Cost == 0 {
		policy.Bcrypt.Cost = defaultBcryptCost
	}

	switch policy.Algorithm {
	case argon2idID, bcryptID:
		return policy, nil
	default:
		return Policy{}, fmt.Errorf("%w: %q", ErrUnknownAlgorithm, policy.Algorithm)
	}
}

// resolveArgon2Params maps config argon2 params onto the policy struct, filling
// any zero-value field with the legacy default so a partially-specified or
// zero-value block is still usable.
func resolveArgon2Params(c config.Argon2Params) Argon2Params {
	p := Argon2Params{
		Memory:     c.Memory,
		Time:       c.Time,
		Threads:    c.Threads,
		KeyLength:  c.KeyLength,
		SaltLength: c.SaltLength,
	}
	if p.Memory == 0 {
		p.Memory = defaultArgon2Params.Memory
	}
	if p.Time == 0 {
		p.Time = defaultArgon2Params.Time
	}
	if p.Threads == 0 {
		p.Threads = defaultArgon2Params.Threads
	}
	if p.KeyLength == 0 {
		p.KeyLength = defaultArgon2Params.KeyLength
	}
	if p.SaltLength == 0 {
		p.SaltLength = defaultArgon2Params.SaltLength
	}

	return p
}
