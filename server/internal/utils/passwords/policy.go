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
		Bcrypt:    BcryptParams{Cost: 12},
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
// Policy. Validation (algorithm in the supported set, cost floors) is performed
// by config.Config.Validate at load time; this returns an error only for a
// genuinely unknown algorithm, as a defensive backstop so an unvalidated config
// can never silently fall back.
func PolicyFromConfig(c config.AuthConfig) (Policy, error) {
	pw := c.Password

	p := Policy{
		Algorithm: pw.Algorithm,
		Argon2: Argon2Params{
			Memory:     pw.Argon2.Memory,
			Time:       pw.Argon2.Time,
			Threads:    pw.Argon2.Threads,
			KeyLength:  pw.Argon2.KeyLength,
			SaltLength: pw.Argon2.SaltLength,
		},
		Bcrypt: BcryptParams{Cost: pw.Bcrypt.Cost},
	}

	switch p.Algorithm {
	case argon2idID, bcryptID:
		return p, nil
	default:
		return Policy{}, fmt.Errorf("%w: %q", ErrUnknownAlgorithm, p.Algorithm)
	}
}
