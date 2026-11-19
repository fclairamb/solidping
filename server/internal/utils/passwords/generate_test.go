package passwords

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGenerate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	first, err := Generate(GeneratedLength)
	r.NoError(err)
	r.Len(first, GeneratedLength)

	for _, c := range first {
		r.True(strings.ContainsRune(generatedAlphabet, c), "unexpected symbol %q", c)
	}

	second, err := Generate(GeneratedLength)
	r.NoError(err)
	r.NotEqual(first, second, "two draws of ~116 bits must differ")
	r.NotContains(generatedAlphabet, "0")
	r.NotContains(generatedAlphabet, "O")
	r.NotContains(generatedAlphabet, "l")
	r.NotContains(generatedAlphabet, "I")
}
