package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
)

// Possession challenges (spec 2026-11-11-02). Before the server hands a sealed
// blob to an agent that says it holds one of the blob's keys, it seals a random
// value to that key, and to nothing else. Only the holder of the matching
// identity can open it and send the value back. The server keeps only the
// SHA-256 of the value, so neither its database nor a response ever carries
// the value itself.

// challengeBytes is the size of the random value of a challenge.
const challengeBytes = 32

// challengeKey is the plaintext key of the value in a sealed challenge.
const challengeKey = "challenge"

// ErrInvalidChallenge is a sealed challenge that does not carry a value.
var ErrInvalidChallenge = errors.New("sealed challenge carries no value")

// NewSealedChallenge seals a fresh random value to one recipient. It returns
// the sealed envelope (for the agent) and the hex SHA-256 of the value (for
// the server, to check the answer with ChallengeAnswered).
func NewSealedChallenge(recipient string) (string, string, error) {
	raw := make([]byte, challengeBytes)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("challenge: %w", err)
	}

	value := hex.EncodeToString(raw)

	sealed, err := SealForRecipients([]string{recipient}, map[string]any{challengeKey: value})
	if err != nil {
		return "", "", err
	}

	return sealed, challengeHash(value), nil
}

// OpenSealedChallenge opens a sealed challenge with the agent's identities and
// returns the value to send back.
func OpenSealedChallenge(sealed string, identities ...string) (string, error) {
	plain, err := UnsealWithIdentities(sealed, identities...)
	if err != nil {
		return "", err
	}

	value, _ := plain[challengeKey].(string)
	if value == "" {
		return "", ErrInvalidChallenge
	}

	return value, nil
}

// ChallengeAnswered reports whether answer is the value whose hash is
// wantHash. An empty hash or answer never matches.
func ChallengeAnswered(answer, wantHash string) bool {
	if answer == "" || wantHash == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(challengeHash(answer)), []byte(wantHash)) == 1
}

func challengeHash(value string) string {
	sum := sha256.Sum256([]byte(value))

	return hex.EncodeToString(sum[:])
}
