package credentials

// These tests live INSIDE the package on purpose: the negative control below
// has to reach decryptWith, the exact function that produced the incident's
// "unknown envelope version".

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// staticParamStore serves one fixed raw JSON value as the stored parameter, so
// a test can pin an exact on-disk shape without a database.
func staticParamStore(raw json.RawMessage) ParamStore {
	return ParamStore{
		Load: func(_ context.Context, _, _ string) (json.RawMessage, bool, error) {
			if raw == nil {
				return nil, false, nil
			}

			return raw, true, nil
		},
		Save: func(_ context.Context, _, _ string, _ any, _ bool) error { return nil },
	}
}

// legacyLoadDEK is the reader as it stood before this fix, reproduced verbatim
// from param_store.go@c54f242a0. It exists so the tests below can prove which
// shapes it mishandled instead of asserting that against a from-memory claim.
func legacyLoadDEK(value json.RawMessage) []byte {
	if len(value) > 0 && value[0] == '"' {
		var unquoted string
		if err := json.Unmarshal(value, &unquoted); err == nil {
			return []byte(unquoted)
		}
	}

	return value
}

func TestLoadDEKAcceptsEveryStoredShape(t *testing.T) {
	t.Parallel()

	envelope := `{"v":1,"alg":"AES-256-GCM","nonce":"bm9uY2U=","ct":"Y3Q="}`

	wrapped, err := json.Marshal(models.ParameterValue(envelope))
	require.NoError(t, err)

	wrappedObject, err := json.Marshal(map[string]any{
		models.ParameterValueKey: json.RawMessage(envelope),
	})
	require.NoError(t, err)

	jsonString, err := json.Marshal(envelope)
	require.NoError(t, err)

	for name, stored := range map[string]json.RawMessage{
		// What SetOrgParameter has always written: the scalar envelope with
		// the DEK envelope inside it as a JSON string.
		"current wrapped scalar": wrapped,
		// Same wrapper, inner value stored as an object rather than a string.
		"wrapped object": wrappedObject,
		// Pre-DEK-store shape: the envelope as a bare JSON string.
		"legacy json string": jsonString,
		// A store that hands back the envelope object untouched.
		"raw envelope object": json.RawMessage(envelope),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			got, found, loadErr := staticParamStore(stored).LoadDEK(t.Context(), "org-1")
			r.NoError(loadErr)
			r.True(found)
			r.JSONEq(envelope, string(got), "every stored shape must yield the same envelope")
		})
	}
}

// TestLoadDEKRejectsAmbiguousShapes pins Proposal item 1's explicit-rejection
// clause: an unrecognizable value must produce a descriptive error naming the
// parameter shape, never ErrUnknownVersion — blaming the envelope version is
// exactly the misdiagnosis this fix removes.
func TestLoadDEKRejectsAmbiguousShapes(t *testing.T) {
	t.Parallel()

	for name, stored := range map[string]json.RawMessage{
		"object with neither value nor v": json.RawMessage(`{"alg":"AES-256-GCM"}`),
		"empty object":                    json.RawMessage(`{}`),
		"nested value wrappers":           json.RawMessage(`{"value":{"value":"x"}}`),
		"json number":                     json.RawMessage(`42`),
		"json null":                       json.RawMessage(`null`),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			_, _, err := staticParamStore(stored).LoadDEK(t.Context(), "org-1")
			r.Error(err)
			r.ErrorIs(err, ErrDEKParamShape)
			r.NotErrorIs(err, ErrUnknownVersion)
		})
	}
}

func TestLoadDEKMissingRowIsNotFound(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got, found, err := staticParamStore(nil).LoadDEK(t.Context(), "org-1")
	r.NoError(err)
	r.False(found)
	r.Nil(got)
}

// TestWrappedDEKFailedOnTheLegacyReader is the NEGATIVE control for this bug.
//
// It builds a genuine wrapped DEK, feeds it to the pre-fix reader, and asserts
// that the crypto layer answers ErrUnknownVersion — the incident's exact
// error — while the fixed reader opens the very same bytes. Without this the
// shape table above would pass just as happily on the broken reader.
func TestWrappedDEKFailedOnTheLegacyReader(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	kek := make([]byte, 32)
	_, err := io.ReadFull(rand.Reader, kek)
	r.NoError(err)

	svc := &service{kek: kek}

	dek := make([]byte, 32)
	_, err = io.ReadFull(rand.Reader, dek)
	r.NoError(err)

	envelope, err := svc.encryptWith(kek, dek)
	r.NoError(err)

	// Exactly what SetOrgParameter puts in the `value` column.
	stored, err := json.Marshal(models.ParameterValue(string(envelope)))
	r.NoError(err)

	// Negative control: the old reader passed the wrapper straight through.
	_, legacyErr := svc.decryptWith(kek, legacyLoadDEK(stored))
	r.ErrorIs(legacyErr, ErrUnknownVersion,
		"the pre-fix reader must fail on the shape SetOrgParameter writes, "+
			"otherwise this test proves nothing about the bug")

	// Positive control: the fixed reader opens the same row.
	got, found, err := staticParamStore(stored).LoadDEK(t.Context(), "org-1")
	r.NoError(err)
	r.True(found)

	unwrapped, err := svc.decryptWith(kek, got)
	r.NoError(err)
	r.Equal(dek, unwrapped)
}
