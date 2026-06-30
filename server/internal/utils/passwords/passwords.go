// Package passwords provides secure, configurable password hashing.
//
// The active hashing algorithm and its cost parameters are chosen from config
// and installed once at startup via SetDefaultPolicy. Stored hashes are
// self-identifying (PHC-style "$argon2id$…" or native bcrypt "$2a$/$2b$/$2y$"),
// so Verify dispatches on the stored marker and old + new algorithms coexist
// with no migration. NeedsRehash reports when a stored hash no longer matches
// the active policy, enabling transparent rehash-on-login.
//
// argon2id remains the default profile (m=64 MiB, t=3, p=4), so the public
// surface (Hash / Verify) is unchanged for existing call sites.
package passwords

import "errors"

// ErrUnknownAlgorithm is returned by PolicyFromConfig for an algorithm outside
// the supported set.
var ErrUnknownAlgorithm = errors.New("unknown password hashing algorithm")

// Hash hashes password using the active default policy (argon2id by default).
func Hash(password string) (string, error) {
	policy := getDefaultPolicy()

	if policy.Algorithm == bcryptID {
		return hashBcrypt(password, policy.Bcrypt)
	}

	// Default / argon2id.
	return hashArgon2id(password, policy.Argon2)
}

// Verify verifies password against a stored hash, dispatching purely on the
// stored hash's self-describing marker. It never consults the active policy, so
// a hash made with any algorithm keeps verifying after the policy changes.
// Unknown / malformed hashes return false.
func Verify(password, hash string) bool {
	switch {
	case isBcryptHash(hash):
		return verifyBcrypt(password, hash)
	default:
		// Either an argon2id hash or something unparseable; verifyArgon2id
		// returns false for the latter.
		return verifyArgon2id(password, hash)
	}
}

// NeedsRehash reports whether a stored hash should be re-minted because its
// algorithm — or any of its cost parameters — differs from the active policy.
// An unparseable / unknown hash returns true so it is upgraded on next login.
func NeedsRehash(hash string) bool {
	policy := getDefaultPolicy()

	storedIsBcrypt := isBcryptHash(hash)
	storedIsArgon2id := !storedIsBcrypt && isArgon2idHash(hash)

	// Unknown / unparseable hash: rehash it.
	if !storedIsBcrypt && !storedIsArgon2id {
		return true
	}

	switch policy.Algorithm {
	case bcryptID:
		if !storedIsBcrypt {
			return true // algorithm changed (argon2id -> bcrypt)
		}
		return bcryptNeedsRehash(hash, policy.Bcrypt)
	default: // argon2id
		if !storedIsArgon2id {
			return true // algorithm changed (bcrypt -> argon2id)
		}
		return argon2idNeedsRehash(hash, policy.Argon2)
	}
}
