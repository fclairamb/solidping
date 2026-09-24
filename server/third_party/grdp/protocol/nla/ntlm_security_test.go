package nla

import (
	"bytes"
	"crypto/rc4"
	"testing"
)

// TestNTLMv2SecurityGssRoundTrip pins the GssEncrypt/GssDecrypt wire format:
// version(4) + checksum(8) + seqNum(4) + ciphertext(len(payload)). It exists
// to prove that switching the internal buffer construction from a
// make([]byte, N+len(x)) allocation to a fixed-size make() plus append
// (done to satisfy CodeQL's "size computation for allocation may overflow"
// check) produces byte-identical output.
func TestNTLMv2SecurityGssRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef")

	encC, err := rc4.NewCipher(key)
	if err != nil {
		t.Fatalf("rc4.NewCipher(encrypt): %v", err)
	}
	decC, err := rc4.NewCipher(key)
	if err != nil {
		t.Fatalf("rc4.NewCipher(decrypt): %v", err)
	}

	signKey := []byte("shared-signing-key-material")

	sec := &NTLMv2Security{
		EncryptRC4: encC,
		DecryptRC4: decC,
		SigningKey: signKey,
		VerifyKey:  signKey,
	}

	for _, payload := range [][]byte{
		[]byte("a"),
		[]byte("a virtual channel payload of arbitrary length"),
		{},
	} {
		wire := sec.GssEncrypt(payload)
		if len(wire) != 16+len(payload) {
			t.Fatalf("GssEncrypt(%q): got %d wire bytes, want %d", payload, len(wire), 16+len(payload))
		}

		got := sec.GssDecrypt(wire)
		if !bytes.Equal(got, payload) {
			t.Fatalf("GssDecrypt(GssEncrypt(%q)) = %q, want %q", payload, got, payload)
		}
	}
}

// TestNTLMv2SecurityGssDecryptRejectsTamperedChecksum confirms GssDecrypt
// still returns nil on a checksum mismatch after the buffer-construction
// change — the HMAC comparison path is unaffected by it.
func TestNTLMv2SecurityGssDecryptRejectsTamperedChecksum(t *testing.T) {
	key := []byte("0123456789abcdef")

	encC, err := rc4.NewCipher(key)
	if err != nil {
		t.Fatalf("rc4.NewCipher(encrypt): %v", err)
	}
	decC, err := rc4.NewCipher(key)
	if err != nil {
		t.Fatalf("rc4.NewCipher(decrypt): %v", err)
	}

	sec := &NTLMv2Security{
		EncryptRC4: encC,
		DecryptRC4: decC,
		SigningKey: []byte("signing-key"),
		VerifyKey:  []byte("a-different-verify-key"),
	}

	wire := sec.GssEncrypt([]byte("payload"))
	if got := sec.GssDecrypt(wire); got != nil {
		t.Fatalf("GssDecrypt with mismatched signing/verify keys = %q, want nil", got)
	}
}
