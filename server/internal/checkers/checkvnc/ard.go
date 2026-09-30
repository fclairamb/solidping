package checkvnc

import (
	"crypto/aes"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
)

// Apple Remote Desktop authentication (RFB security type 30), as spoken by
// macOS Screen Sharing and implemented by noVNC and libvncclient:
//
//	server -> generator (uint16), key length (uint16), prime, server public key
//	client -> AES-128-ECB(MD5(shared secret), credentials) || client public key
//
// The 128-byte credentials block holds the username in its first 64 bytes
// and the password in its last 64, each null-terminated, the rest random.

const (
	ardCredentialsLen = 128
	ardFieldLen       = 64
	// ardMinKeyLen / ardMaxKeyLen bound the DH modulus a server may impose
	// (macOS uses 128 bytes): a hostile server must not make us allocate
	// or exponentiate absurd sizes.
	ardMinKeyLen = 16
	ardMaxKeyLen = 1024
)

var errARDParams = errors.New("invalid Apple Remote Desktop DH parameters")

// ardParams are the server's Diffie-Hellman parameters.
type ardParams struct {
	generator *big.Int
	prime     *big.Int
	serverPub *big.Int
	keyLen    int
}

// readARDParams reads and sanity-checks the server's DH parameters.
func readARDParams(reader io.Reader) (*ardParams, error) {
	var header [4]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, fmt.Errorf("read ARD parameters: %w", err)
	}

	keyLen := int(binary.BigEndian.Uint16(header[2:4]))
	if keyLen < ardMinKeyLen || keyLen > ardMaxKeyLen {
		return nil, failure(FailureProtocol, nil, "Apple Remote Desktop key length %d out of range", keyLen)
	}

	body := make([]byte, 2*keyLen)
	if _, err := io.ReadFull(reader, body); err != nil {
		return nil, fmt.Errorf("read ARD parameters: %w", err)
	}

	params := &ardParams{
		generator: new(big.Int).SetBytes(header[0:2]),
		prime:     new(big.Int).SetBytes(body[:keyLen]),
		serverPub: new(big.Int).SetBytes(body[keyLen:]),
		keyLen:    keyLen,
	}

	if err := params.check(); err != nil {
		return nil, failure(FailureProtocol, err, "%v", err)
	}

	return params, nil
}

// check rejects degenerate parameters: a generator or public key of 0, 1 or
// p-1 would make the shared secret predictable.
func (p *ardParams) check() error {
	two := big.NewInt(2)
	pMinus1 := new(big.Int).Sub(p.prime, big.NewInt(1))

	switch {
	case p.prime.Cmp(big.NewInt(5)) < 0:
		return fmt.Errorf("%w: prime too small", errARDParams)
	case p.generator.Cmp(two) < 0 || p.generator.Cmp(pMinus1) >= 0:
		return fmt.Errorf("%w: generator out of range", errARDParams)
	case p.serverPub.Cmp(two) < 0 || p.serverPub.Cmp(pMinus1) >= 0:
		return fmt.Errorf("%w: server public key out of range", errARDParams)
	default:
		return nil
	}
}

// ardPrivateKey draws a private exponent in [2, p-2].
func ardPrivateKey(params *ardParams, rng io.Reader) (*big.Int, error) {
	limit := new(big.Int).Sub(params.prime, big.NewInt(3))

	priv, err := rand.Int(rng, limit)
	if err != nil {
		return nil, fmt.Errorf("ARD private key: %w", err)
	}

	return priv.Add(priv, big.NewInt(2)), nil
}

// ardResponse builds the client message: the encrypted credentials block
// followed by the client public key, both at the server's key length.
// padding supplies the 128 random filler bytes (deterministic in tests).
func ardResponse(params *ardParams, username, password string, priv *big.Int, padding io.Reader) ([]byte, error) {
	clientPub := new(big.Int).Exp(params.generator, priv, params.prime)
	shared := new(big.Int).Exp(params.serverPub, priv, params.prime)

	key := md5.Sum(shared.FillBytes(make([]byte, params.keyLen)))

	credentials := make([]byte, ardCredentialsLen)
	if _, err := io.ReadFull(padding, credentials); err != nil {
		return nil, fmt.Errorf("ARD padding: %w", err)
	}

	putARDField(credentials[:ardFieldLen], username)
	putARDField(credentials[ardFieldLen:], password)

	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}

	out := make([]byte, ardCredentialsLen, ardCredentialsLen+params.keyLen)
	for i := 0; i < ardCredentialsLen; i += aes.BlockSize { // ECB: block by block
		block.Encrypt(out[i:i+aes.BlockSize], credentials[i:i+aes.BlockSize])
	}

	return append(out, clientPub.FillBytes(make([]byte, params.keyLen))...), nil
}

// putARDField writes value (truncated to 63 bytes) and its null terminator
// at the start of a 64-byte field, keeping the random filler after it.
func putARDField(field []byte, value string) {
	n := copy(field[:ardFieldLen-1], value)
	field[n] = 0
}

// ardAuthenticate runs the exchange after security type 30 was selected.
// The caller reads SecurityResult.
func ardAuthenticate(stream io.ReadWriter, username, password string, rng io.Reader) error {
	params, err := readARDParams(stream)
	if err != nil {
		return err
	}

	priv, err := ardPrivateKey(params, rng)
	if err != nil {
		return err
	}

	response, err := ardResponse(params, username, password, priv, rng)
	if err != nil {
		return err
	}

	if _, err := stream.Write(response); err != nil {
		return fmt.Errorf("write ARD response: %w", err)
	}

	return nil
}
