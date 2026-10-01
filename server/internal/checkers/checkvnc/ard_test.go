package checkvnc

import (
	"bytes"
	"crypto/aes"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// oakleyGroup2 is the RFC 2409 1024-bit MODP prime (generator 2): a 128-byte
// key, the size macOS uses.
const oakleyGroup2 = "FFFFFFFFFFFFFFFFC90FDAA22168C234C4C6628B80DC1CD1" +
	"29024E088A67CC74020BBEA63B139B22514A08798E3404DD" +
	"EF9519B3CD3A431B302B0A6DF25F14374FE1356D6D51C245" +
	"E485B576625E7EC6F44C42E9A637ED6B0BFF5CB6F406B7ED" +
	"EE386BFB5A899FA5AE9F24117C4B1FE649286651ECE65381" +
	"FFFFFFFFFFFFFFFF"

func testARDPrime() *big.Int {
	p, _ := new(big.Int).SetString(oakleyGroup2, 16)

	return p
}

// fixedPadding is a deterministic stand-in for the random filler: 00 01 .. 7f.
func fixedPadding() *bytes.Reader {
	pad := make([]byte, ardCredentialsLen)
	for i := range pad {
		pad[i] = byte(i)
	}

	return bytes.NewReader(pad)
}

// ardServerMessage is the server's opening: generator, key length, prime,
// public key.
func ardServerMessage(prime, serverPub *big.Int) []byte {
	keyLen := (prime.BitLen() + 7) / 8
	out := append(u16(2), u16(uint16(keyLen))...)
	out = append(out, prime.FillBytes(make([]byte, keyLen))...)

	return append(out, serverPub.FillBytes(make([]byte, keyLen))...)
}

// ardDecrypt is the server side: derive the shared key from the client's
// public key and decrypt the credentials block.
func ardDecrypt(prime, serverPriv *big.Int, msg []byte) (string, string, error) {
	keyLen := (prime.BitLen() + 7) / 8
	if len(msg) != ardCredentialsLen+keyLen {
		return "", "", fmt.Errorf("%w: ARD response is %d bytes", errFakeServer, len(msg))
	}

	clientPub := new(big.Int).SetBytes(msg[ardCredentialsLen:])
	shared := new(big.Int).Exp(clientPub, serverPriv, prime)
	key := md5.Sum(shared.FillBytes(make([]byte, keyLen)))

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", "", err
	}

	plain := make([]byte, ardCredentialsLen)
	for i := 0; i < ardCredentialsLen; i += aes.BlockSize {
		block.Decrypt(plain[i:i+aes.BlockSize], msg[i:i+aes.BlockSize])
	}

	field := func(b []byte) string {
		if i := bytes.IndexByte(b, 0); i >= 0 {
			return string(b[:i])
		}

		return string(b)
	}

	return field(plain[:ardFieldLen]), field(plain[ardFieldLen:]), nil
}

// TestARDGoldenVector: fixed DH parameters, private key and padding give a
// known message. The vector was computed once with this implementation and
// is cross-checked below by an independent server-side decryption, so a
// regression in the layout (field offsets, terminators, key derivation,
// ciphertext/public-key order) changes it.
func TestARDGoldenVector(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	prime := testARDPrime()
	serverPriv := big.NewInt(0x5eed)
	clientPriv := big.NewInt(0xc0ffee)

	params := &ardParams{
		generator: big.NewInt(2), prime: prime, keyLen: 128,
		serverPub: new(big.Int).Exp(big.NewInt(2), serverPriv, prime),
	}

	msg, err := ardResponse(params, "alice", "s3cret", clientPriv, fixedPadding())
	r.NoError(err)
	r.Len(msg, ardCredentialsLen+128)

	r.Equal("f2a2dfaa8e226dd035763e1199db5d35add3d7450bc5029c33991377b2ddc0ee", hex.EncodeToString(msg[:32]))

	sum := sha256.Sum256(msg)
	r.Equal("71b40268f022dc4db698295cf6082c53540e4f752f71629799cbd9fbd8d80115", hex.EncodeToString(sum[:]))

	// The client public key is g^priv mod p, big-endian at the key length.
	r.Equal(new(big.Int).Exp(big.NewInt(2), clientPriv, prime).FillBytes(make([]byte, 128)), msg[ardCredentialsLen:])

	user, pass, err := ardDecrypt(prime, serverPriv, msg)
	r.NoError(err)
	r.Equal("alice", user)
	r.Equal("s3cret", pass)
}

// TestARDPaddingKeptAfterTerminator: the filler after each null terminator
// is the padding, not zeroes.
func TestARDPaddingKeptAfterTerminator(t *testing.T) {
	t.Parallel()

	block := make([]byte, ardFieldLen)
	for i := range block {
		block[i] = 0xaa
	}

	putARDField(block, "bob")
	require.Equal(t, []byte{'b', 'o', 'b', 0, 0xaa}, block[:5])

	putARDField(block, strings.Repeat("x", 100))
	require.Equal(t, byte(0), block[ardFieldLen-1], "a long value is truncated to 63 bytes + terminator")
}

// ardServer scripts a 3.8 server offering ARD (plus extra types), checking
// the decrypted credentials.
type ardServer struct {
	extraTypes []byte
	username   string
	password   string
}

func (s *ardServer) run(conn net.Conn) error {
	if err := rfbGreeting(conn, append([]byte{secARD}, s.extraTypes...)...); err != nil {
		return err
	}

	chosen, err := readN(conn, 1)
	if err != nil {
		return err
	}

	if chosen[0] != secARD {
		return fmt.Errorf("%w: client chose %d, want ARD", errFakeServer, chosen[0])
	}

	prime := testARDPrime()
	serverPriv := big.NewInt(0xabcdef)

	if err = writeAll(conn, ardServerMessage(prime, new(big.Int).Exp(big.NewInt(2), serverPriv, prime))); err != nil {
		return err
	}

	msg, err := readN(conn, ardCredentialsLen+128)
	if err != nil {
		return err
	}

	user, pass, err := ardDecrypt(prime, serverPriv, msg)
	if err != nil {
		return err
	}

	if user != s.username || pass != s.password {
		return writeAll(conn, u32(1), reasonBytes("Authentication failed"))
	}

	if err = writeAll(conn, u32(0)); err != nil {
		return err
	}

	if _, err = readN(conn, 1); err != nil {
		return err
	}

	return writeAll(conn, serverInitBytes(testW, testH, "mac"))
}

// TestARDFakeServer: the server decrypts the block and accepts the right
// credentials; a wrong password is AUTH_FAILED.
func TestARDFakeServer(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	good := &ardServer{extraTypes: []byte{secVNCAuth}, username: "alice", password: "a long mac password"}
	host, port, errs := fakeServer(t, good.run)

	up := runCheck(t, &VNCConfig{Host: host, Port: port, Username: "alice", Password: "a long mac password"})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusUp, up.Status, up.Output)
	r.Equal("Apple Remote Desktop", up.Output["securityType"])
	r.Equal("mac", up.Output["desktopName"])

	bad := &ardServer{username: "alice", password: "right"}
	host2, port2, errs2 := fakeServer(t, bad.run)

	down := runCheck(t, &VNCConfig{Host: host2, Port: port2, Username: "alice", Password: "wrong"})
	r.NoError(serverErr(t, errs2))
	r.Equal(checkerdef.StatusDown, down.Status)
	r.Equal(string(FailureAuthFailed), down.Output["failure_code"])
	r.Contains(down.Output["error"], "Authentication failed")
}

// TestARDRejectsDegenerateParams: a server key length out of range or a
// public key of 1 is a protocol error, not an exponentiation.
func TestARDRejectsDegenerateParams(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	_, err := readARDParams(bytes.NewReader(append(u16(2), u16(4096)...)))
	r.ErrorContains(err, "key length")

	_, err = readARDParams(bytes.NewReader(ardServerMessage(testARDPrime(), big.NewInt(1))))
	r.ErrorIs(err, errARDParams)

	params, err := readARDParams(bytes.NewReader(ardServerMessage(testARDPrime(), big.NewInt(12345))))
	r.NoError(err, "positive control")
	r.Equal(128, params.keyLen)
}
