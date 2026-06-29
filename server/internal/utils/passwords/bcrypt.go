package passwords

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/bcrypt"
)

// bcryptID is the logical algorithm name used in config/policy. Stored bcrypt
// hashes are self-identifying via their native "$2a$/$2b$/$2y$" prefix.
const bcryptID = "bcrypt"

// bcryptPrefixes are the native bcrypt PHC markers we recognize on the stored
// hash. Note "$bcrypt$" is deliberately NOT here — it is not a native bcrypt
// prefix and must stay invalid.
//
//nolint:gochecknoglobals // immutable lookup table of bcrypt prefixes
var bcryptPrefixes = []string{"$2a$", "$2b$", "$2y$"}

// isBcryptHash reports whether hash carries a native bcrypt marker.
func isBcryptHash(hash string) bool {
	for _, p := range bcryptPrefixes {
		if strings.HasPrefix(hash, p) {
			return true
		}
	}
	return false
}

// bcryptPrehash maps an arbitrary-length password to a fixed 44-char,
// NUL-free ASCII string via base64(sha256(password)). golang.org/x/crypto/bcrypt
// errors on inputs >72 bytes and truncates at embedded NUL bytes; pre-hashing
// sidesteps both. Our bcrypt hashes are only ever produced and consumed here, so
// self-consistency is all that is required.
func bcryptPrehash(password string) []byte {
	sum := sha256.Sum256([]byte(password))
	encoded := base64.RawStdEncoding.EncodeToString(sum[:])
	return []byte(encoded)
}

// hashBcrypt derives a native bcrypt hash of password at the given cost,
// applying the sha256+base64 pre-hash.
func hashBcrypt(password string, p BcryptParams) (string, error) {
	out, err := bcrypt.GenerateFromPassword(bcryptPrehash(password), p.Cost)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// verifyBcrypt verifies password against a stored bcrypt hash, replaying the
// same pre-hash used when minting it.
func verifyBcrypt(password, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), bcryptPrehash(password)) == nil
}

// bcryptNeedsRehash reports whether a stored bcrypt hash's cost differs from the
// target cost (or the hash is unparseable as bcrypt).
func bcryptNeedsRehash(hash string, target BcryptParams) bool {
	cost, err := bcrypt.Cost([]byte(hash))
	if err != nil {
		return true
	}
	return cost != target.Cost
}
