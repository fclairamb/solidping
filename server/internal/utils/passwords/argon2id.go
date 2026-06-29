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

// argon2idID is the algorithm marker for argon2id hashes (the leading
// "$argon2id$" segment of the PHC-style stored string).
const argon2idID = "argon2id"

// maxParallelismValue is the maximum value for the uint8 parallelism parameter.
const maxParallelismValue = 255

// maxConcurrentHashes caps how many argon2id derivations run at once. Each one
// transiently allocates the configured argon2 memory (64 MiB by default), so
// without a bound a burst of concurrent logins could spike memory by memory × N.
// Capping at a small multiple of the CPU count bounds the worst case
// (peak ≈ cap × memory) while still letting independent logins proceed in
// parallel. The trade-off is that excess concurrent auth requests queue briefly
// rather than all allocating at once.
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
// number of simultaneous large derivations.
func boundedIDKey(password, salt []byte, time, memory uint32, threads uint8, keyLen uint32) []byte {
	hashSem <- struct{}{}
	defer func() { <-hashSem }()

	return argon2.IDKey(password, salt, time, memory, threads, keyLen)
}

// hashArgon2id derives an argon2id PHC-style hash of password using the supplied
// params. Format: $argon2id$v=19$m=<mem>,t=<time>,p=<threads>$salt$hash.
func hashArgon2id(password string, p Argon2Params) (string, error) {
	salt := make([]byte, p.SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}

	hash := boundedIDKey([]byte(password), salt, p.Time, p.Memory, p.Threads, p.KeyLength)

	saltB64 := base64.RawStdEncoding.EncodeToString(salt)
	hashB64 := base64.RawStdEncoding.EncodeToString(hash)

	return fmt.Sprintf(
		"$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", p.Memory, p.Time, p.Threads, saltB64, hashB64,
	), nil
}

// argon2idParsed holds the cost parameters decoded from a stored argon2id hash.
type argon2idParsed struct {
	memory      uint32
	time        uint32
	parallelism uint32
	keyLength   uint32
}

// parseArgon2id decodes the PHC-style argon2id string into its salt, stored hash
// and cost parameters. ok is false when the string is not a well-formed
// argon2id hash.
func parseArgon2id(hash string) (salt, stored []byte, params argon2idParsed, ok bool) {
	// Format: $argon2id$v=19$m=65536,t=3,p=4$salt$hash
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[1] != argon2idID {
		return nil, nil, argon2idParsed{}, false
	}

	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.time, &params.parallelism); err != nil {
		return nil, nil, argon2idParsed{}, false
	}

	// argon2 expects a uint8 parallelism.
	if params.parallelism > maxParallelismValue {
		return nil, nil, argon2idParsed{}, false
	}

	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return nil, nil, argon2idParsed{}, false
	}

	stored, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return nil, nil, argon2idParsed{}, false
	}

	// len(stored) is the key length, typically 32 bytes, safe to convert to uint32.
	params.keyLength = uint32(len(stored))

	return salt, stored, params, true
}

// verifyArgon2id verifies password against a stored argon2id hash. Verification
// is parameter-independent: the cost parameters are read back out of the stored
// string, so an old hash keeps verifying with its own embedded m/t/p.
func verifyArgon2id(password, hash string) bool {
	salt, stored, params, ok := parseArgon2id(hash)
	if !ok {
		return false
	}

	// parallelism is validated to be <= 255, safe to convert to uint8.
	computed := boundedIDKey([]byte(password), salt, params.time, params.memory, uint8(params.parallelism), params.keyLength)

	return subtle.ConstantTimeCompare(stored, computed) == 1
}

// argon2idNeedsRehash reports whether a stored argon2id hash's cost parameters
// differ from the target argon2 params (i.e. the hash should be re-minted).
func argon2idNeedsRehash(hash string, target Argon2Params) bool {
	_, _, params, ok := parseArgon2id(hash)
	if !ok {
		return true
	}

	return params.memory != target.Memory ||
		params.time != target.Time ||
		uint8(params.parallelism) != target.Threads ||
		params.keyLength != target.KeyLength
}
