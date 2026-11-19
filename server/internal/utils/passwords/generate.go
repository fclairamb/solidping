package passwords

import (
	"crypto/rand"
	"fmt"
	"math/big"
)

// generatedAlphabet leaves out the characters that read alike in a terminal
// font (0/O/o, 1/l/I), so a password copied by hand from a log line is typed
// right the first time. 56 symbols: about 5.8 bits each.
const generatedAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"

// GeneratedLength is the length Generate is called with for the first-boot
// superadmin password: 20 symbols of generatedAlphabet, about 116 bits.
const GeneratedLength = 20

// Generate returns a random password of length symbols drawn uniformly from an
// unambiguous alphabet with crypto/rand.
func Generate(length int) (string, error) {
	limit := big.NewInt(int64(len(generatedAlphabet)))
	out := make([]byte, length)

	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generating random password: %w", err)
		}

		out[i] = generatedAlphabet[n.Int64()]
	}

	return string(out), nil
}
