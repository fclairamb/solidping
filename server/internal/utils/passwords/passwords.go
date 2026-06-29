// Package passwords provides secure password hashing using argon2id.
package passwords

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters.
const (
	saltLength          = 16
	argon2Memory        = 64 * 1024 // 64 MB
	argon2Time          = 3
	argon2Threads       = 4
	keyLength           = 32
	maxParallelismValue = 255 // Maximum value for uint8 parallelism parameter
)

// maxConcurrentHashes caps how many argon2id derivations run at once. Each one
// transiently allocates argon2Memory (64 MB), so without a bound a burst of
// concurrent logins could spike memory by 64 MB × N. Capping at a small multiple
// of the CPU count bounds the worst case (peak ≈ cap × 64 MB) while still letting
// independent logins proceed in parallel. The trade-off is that excess
// concurrent auth requests queue briefly rather than all allocating at once.
const maxConcurrentHashes = 4

// hashSem bounds concurrent argon2 derivations. Sized once at package init to
// min(GOMAXPROCS, maxConcurrentHashes); a buffered channel is the idiomatic
// counting semaphore.
//
//nolint:gochecknoglobals // process-wide counting semaphore bounding peak argon2 memory
var hashSem = make(chan struct{}, hashConcurrency())

func hashConcurrency() int {
	n := runtime.GOMAXPROCS(0)
	if n > maxConcurrentHashes {
		n = maxConcurrentHashes
	}
	if n < 1 {
		n = 1
	}
	return n
}

// boundedIDKey runs argon2.IDKey while holding a hashSem slot, bounding the
// number of simultaneous 64 MB derivations.
func boundedIDKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	hashSem <- struct{}{}
	defer func() { <-hashSem }()

	return argon2.IDKey(password, salt, time, memory, threads, keyLen)
}

// Hash creates an argon2id hash of the password.
func Hash(password string) (string, error) {
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	hash := boundedIDKey([]byte(password), salt, argon2Time, argon2Memory, argon2Threads, keyLength)

	// Encode salt and hash as base64
	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)

	// Format: $argon2id$v=19$m=65536,t=3,p=4$salt$hash
	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argon2Memory, argon2Time, argon2Threads, saltB64, hashB64,
	), nil
}

// Verify verifies a password against an argon2id hash.
func Verify(password, hash string) bool {
	// Parse the hash format: $argon2id$v=19$m=65536,t=3,p=4$salt$hash
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}

	// Parse parameters
	var memory, timeCost, parallelism uint32
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &timeCost, &parallelism); err != nil {
		return false
	}

	// Validate parallelism fits in uint8 (argon2 expects uint8)
	if parallelism > maxParallelismValue {
		return false
	}

	// Decode salt and stored hash
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}

	storedHash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}

	// Compute hash with same parameters
	// parallelism is validated to be <= 255, safe to convert to uint8
	// len(storedHash) is the key length, typically 32 bytes, safe to convert to uint32
	computedHash := boundedIDKey([]byte(password), salt, timeCost, memory, uint8(parallelism), uint32(len(storedHash)))

	// Use constant-time comparison
	return subtle.ConstantTimeCompare(storedHash, computedHash) == 1
}
