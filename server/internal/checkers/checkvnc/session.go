package checkvnc

import (
	"bufio"
	"bytes"
	"context"
	"crypto/des"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"strings"
)

// The authenticated path: security-type selection, VNC authentication,
// SecurityResult, ClientInit/ServerInit and the one-frame screenshot.

const (
	challengeLen = 16
	desKeyLen    = 8

	// maxDesktopNameLen caps the ServerInit name.
	maxDesktopNameLen = 4096
	// maxFramePixels caps the framebuffer a screenshot allocates (a 5K
	// desktop is 14.7 M pixels): a hostile ServerInit must not make the
	// worker allocate gigabytes.
	maxFramePixels = 16 * 1024 * 1024
	// maxCutTextLen caps a ServerCutText payload we are willing to skip.
	maxCutTextLen = 16 * 1024 * 1024

	bytesPerPixel = 4

	// Client-to-server message types.
	msgSetPixelFormat           = 0
	msgSetEncodings             = 2
	msgFramebufferUpdateRequest = 3

	// Server-to-client message types.
	msgFramebufferUpdate  = 0
	msgSetColorMapEntries = 1
	msgBell               = 2
	msgServerCutText      = 3

	// Encodings.
	encodingRaw      int32 = 0
	encodingCopyRect int32 = 1

	// SecurityResult values. 0 is OK and 1 is failed (RFC 6143); several
	// servers use 2 for "too many attempts".
	securityResultOK      = 0
	securityResultTooMany = 2
)

// failureCode is the stable machine code a failed run reports in its
// `failure_code` output key.
type failureCode string

const (
	// FailureConnection is a TCP connect failure (refused, unreachable).
	FailureConnection failureCode = "CONNECTION_FAILED"
	// FailureNotRFB is a listener that does not speak RFB.
	FailureNotRFB failureCode = "NOT_RFB"
	// FailureServerRefused is an empty security-types list with a reason.
	FailureServerRefused failureCode = "SERVER_REFUSED"
	// FailureNoAuthOffered is a server offering security type None while
	// requireAuth is on: anyone can attach to the desktop.
	FailureNoAuthOffered failureCode = "NO_AUTH_OFFERED"
	// FailureAuthUnsupported is a password configured but no security type
	// this checker can authenticate with (e.g. only RA2, ARD without a
	// username, or VeNCrypt with only anonymous-TLS sub-types).
	FailureAuthUnsupported failureCode = "AUTH_TYPE_UNSUPPORTED"
	// FailureAuthFailed is a rejected password.
	FailureAuthFailed failureCode = "AUTH_FAILED"
	// FailureTooManyAttempts is the server's brute-force lockout.
	FailureTooManyAttempts failureCode = "TOO_MANY_ATTEMPTS"
	// FailureNoFrame is a screenshot whose frame never arrived in time.
	FailureNoFrame failureCode = "NO_FRAME"
	// FailureTLS is a failed VeNCrypt TLS handshake, including an
	// untrusted certificate when tlsVerify is on.
	FailureTLS failureCode = "TLS_FAILED"
	// FailureCertExpiry is a VeNCrypt certificate expired or inside the
	// warning/critical thresholds.
	FailureCertExpiry failureCode = "CERT_EXPIRY"
	// FailureProtocol is any other malformed or unexpected exchange.
	FailureProtocol failureCode = "PROTOCOL_ERROR"
)

// FailureError is a classified check failure.
type FailureError struct {
	Code failureCode
	Msg  string
	// Cause is the underlying error, if any (kept for timeout detection).
	Cause error
}

func (e *FailureError) Error() string { return e.Msg }

func (e *FailureError) Unwrap() error { return e.Cause }

func failure(code failureCode, cause error, format string, args ...any) *FailureError {
	return &FailureError{Code: code, Msg: fmt.Sprintf(format, args...), Cause: cause}
}

// serverInit is the parsed ServerInit message.
type serverInit struct {
	width, height uint16
	name          string
}

// reverseBits mirrors the bit order of one byte. VNC authentication uses the
// password as a DES key with every byte bit-reversed: a quirk of the original
// AT&T implementation that every server and client now depends on.
func reverseBits(b byte) byte {
	var out byte
	for i := range 8 {
		out = out<<1 | (b>>i)&1
	}

	return out
}

// vncAuthKey builds the DES key: the password truncated or zero-padded to 8
// bytes, each byte bit-reversed.
func vncAuthKey(password string) []byte {
	key := make([]byte, desKeyLen)
	copy(key, password)

	for i := range key {
		key[i] = reverseBits(key[i])
	}

	return key
}

// vncAuthResponse encrypts the 16-byte challenge (two DES-ECB blocks) with the
// password-derived key.
func vncAuthResponse(password string, challenge []byte) ([]byte, error) {
	if len(challenge) != challengeLen {
		return nil, fmt.Errorf("%w: got %d bytes", errBadChallenge, len(challenge))
	}

	block, err := des.NewCipher(vncAuthKey(password))
	if err != nil {
		return nil, fmt.Errorf("des: %w", err)
	}

	response := make([]byte, challengeLen)
	block.Encrypt(response[:desKeyLen], challenge[:desKeyLen])
	block.Encrypt(response[desKeyLen:], challenge[desKeyLen:])

	return response, nil
}

// authOutcome is what a security negotiation leaves behind.
type authOutcome struct {
	// conn is the stream to continue on: TLS-wrapped after VeNCrypt.
	conn    net.Conn
	secType uint8
	// subtype is the VeNCrypt sub-type, 0 for other types.
	subtype uint32
	// cert is the VeNCrypt X.509 leaf certificate, kept even when the
	// handshake failed verification so it can still be reported.
	cert *x509.Certificate
}

// redialFunc opens a fresh connection and re-runs the handshake. It is how
// a run falls back when VeNCrypt, once selected, offers no usable sub-type:
// RFB has no way back to the type list on the same connection.
type redialFunc func() (net.Conn, *handshakeResult, error)

// secure authenticates with the strongest candidate type, falling back to
// the next one on a fresh connection (when redial is set) if VeNCrypt turns
// out unusable. The outcome may be non-nil on error (it carries the TLS
// certificate for the report). A connection secure opened itself is closed
// on error; on success the caller closes outcome.conn.
func secure(
	ctx context.Context, conn net.Conn, offer *handshakeResult, cfg *VNCConfig, redial redialFunc,
) (*authOutcome, error) {
	candidates, err := securityCandidates(offer, cfg)
	if err != nil {
		return nil, err
	}

	redialed := false

	for i, secType := range candidates {
		outcome, authErr := authenticate(ctx, conn, offer, secType, cfg)
		if authErr == nil {
			return outcome, nil
		}

		last := i == len(candidates)-1
		if !errors.Is(authErr, errNoUsableSubtype) || last || redial == nil {
			if redialed {
				_ = conn.Close()
			}

			return outcome, authErr
		}

		_ = conn.Close()

		conn, offer, err = redial()
		if err != nil {
			return nil, err
		}

		redialed = true
	}

	// Unreachable: the loop returns on its last candidate.
	return nil, failure(FailureProtocol, nil, "no authentication method left to try")
}

// authenticate selects secType (3.7+), runs its authentication and reads
// SecurityResult where the protocol version sends one.
func authenticate(
	ctx context.Context, conn net.Conn, offer *handshakeResult, secType uint8, cfg *VNCConfig,
) (*authOutcome, error) {
	if offer.version != version33 {
		if _, err := conn.Write([]byte{secType}); err != nil {
			return nil, fmt.Errorf("write security type: %w", err)
		}
	}

	outcome := &authOutcome{conn: conn, secType: secType}

	switch secType {
	case secVNCAuth:
		if err := vncAuthenticate(conn, cfg.Password); err != nil {
			return nil, err
		}
	case secVeNCrypt:
		if err := vencryptAuthenticate(ctx, outcome, cfg); err != nil {
			return outcome, err
		}
	case secARD:
		if err := ardAuthenticate(conn, cfg.Username, cfg.Password, rand.Reader); err != nil {
			return nil, err
		}
	default:
		if offer.version != version38 {
			// None on 3.3/3.7: no SecurityResult, straight to ClientInit.
			return outcome, nil
		}
	}

	return outcome, readSecurityResult(outcome.conn, offer.version)
}

// vncAuthenticate answers the type-2 DES challenge.
func vncAuthenticate(stream io.ReadWriter, password string) error {
	challenge := make([]byte, challengeLen)
	if _, err := io.ReadFull(stream, challenge); err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}

	response, err := vncAuthResponse(password, challenge)
	if err != nil {
		return err
	}

	if _, err := stream.Write(response); err != nil {
		return fmt.Errorf("write challenge response: %w", err)
	}

	return nil
}

// bufferedConn is a connection whose reads go through a buffered reader, so
// a TLS upgrade or a redial keeps whatever the reader already buffered.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func newBufferedConn(conn net.Conn) *bufferedConn {
	return &bufferedConn{Conn: conn, reader: bufio.NewReader(conn)}
}

func (b *bufferedConn) Read(p []byte) (int, error) { return b.reader.Read(p) } //nolint:wrapcheck // passthrough

// readSecurityResult reads the uint32 result and, on 3.8, the reason string.
func readSecurityResult(reader io.Reader, version rfbVersion) error {
	var result uint32
	if err := binary.Read(reader, binary.BigEndian, &result); err != nil {
		return fmt.Errorf("read security result: %w", err)
	}

	if result == securityResultOK {
		return nil
	}

	reason := ""
	if version == version38 {
		reason = readReason(reader)
	}

	code := FailureAuthFailed
	if result == securityResultTooMany || isLockoutReason(reason) {
		code = FailureTooManyAttempts
	}

	msg := "authentication failed"
	if code == FailureTooManyAttempts {
		msg = "authentication refused: too many attempts"
	}

	if reason != "" {
		msg += ": " + reason
	}

	return failure(code, nil, "%s", msg)
}

// isLockoutReason recognizes the brute-force lockout messages of the common
// servers ("Too many security failures" from RealVNC, "Too many
// authentication failures" from TightVNC, blacklisting from x11vnc/libvncserver).
func isLockoutReason(reason string) bool {
	lower := strings.ToLower(reason)

	return strings.Contains(lower, "too many") || strings.Contains(lower, "blacklist")
}

// clientServerInit sends ClientInit with shared-flag = 1 (never kick an
// existing viewer) and parses ServerInit.
func clientServerInit(stream io.ReadWriter) (*serverInit, error) {
	if _, err := stream.Write([]byte{1}); err != nil {
		return nil, fmt.Errorf("write client init: %w", err)
	}

	// width(2) height(2) pixel-format(16) name-length(4)
	header := make([]byte, 24)
	if _, err := io.ReadFull(stream, header); err != nil {
		return nil, fmt.Errorf("read server init: %w", err)
	}

	init := &serverInit{
		width:  binary.BigEndian.Uint16(header[0:2]),
		height: binary.BigEndian.Uint16(header[2:4]),
	}

	nameLen := binary.BigEndian.Uint32(header[20:24])
	if nameLen > maxDesktopNameLen {
		return nil, failure(FailureProtocol, nil, "desktop name too long (%d bytes)", nameLen)
	}

	name := make([]byte, nameLen)
	if _, err := io.ReadFull(stream, name); err != nil {
		return nil, fmt.Errorf("read desktop name: %w", err)
	}

	init.name = string(name)

	return init, nil
}

// requestFrame sets a known 32 bpp true-color pixel format, advertises Raw
// and CopyRect only, and asks for one non-incremental full-screen update.
func requestFrame(writer io.Writer, width, height uint16) error {
	var buf bytes.Buffer

	// SetPixelFormat: type, 3 padding, then the 16-byte PIXEL_FORMAT:
	// bpp 32, depth 24, little-endian, true-color, maxes 255, shifts R16 G8 B0.
	buf.Write([]byte{msgSetPixelFormat, 0, 0, 0})
	buf.Write([]byte{32, 24, 0, 1})
	_ = binary.Write(&buf, binary.BigEndian, [3]uint16{255, 255, 255})
	buf.Write([]byte{16, 8, 0, 0, 0, 0})

	// SetEncodings: type, padding, count, encodings.
	buf.Write([]byte{msgSetEncodings, 0})
	_ = binary.Write(&buf, binary.BigEndian, uint16(2))
	_ = binary.Write(&buf, binary.BigEndian, [2]int32{encodingRaw, encodingCopyRect})

	// FramebufferUpdateRequest: type, incremental=0, x, y, w, h.
	buf.Write([]byte{msgFramebufferUpdateRequest, 0})
	_ = binary.Write(&buf, binary.BigEndian, [4]uint16{0, 0, width, height})

	if _, err := writer.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("write frame request: %w", err)
	}

	return nil
}

// errBadChallenge is a VNC-auth challenge that is not 16 bytes.
var errBadChallenge = errors.New("vnc auth challenge must be 16 bytes")

// errNoFrame is a frame read that ended before any framebuffer update.
var errNoFrame = errors.New("no frame received")

// readFrame reads server messages until the first FramebufferUpdate carrying
// at least one rectangle, decoding it into an RGBA image. Bell,
// SetColorMapEntries and ServerCutText are skipped; an unknown message type
// or encoding fails cleanly (the stream cannot be resynchronized).
func readFrame(reader io.Reader, width, height uint16) (*image.RGBA, error) {
	if int(width)*int(height) > maxFramePixels {
		return nil, failure(FailureProtocol, nil, "framebuffer %dx%d too large to capture", width, height)
	}

	if width == 0 || height == 0 {
		return nil, failure(FailureProtocol, nil, "empty framebuffer %dx%d", width, height)
	}

	img := image.NewRGBA(image.Rect(0, 0, int(width), int(height)))

	for {
		var msgType [1]byte
		if _, err := io.ReadFull(reader, msgType[:]); err != nil {
			return nil, fmt.Errorf("%w: %w", errNoFrame, err)
		}

		switch msgType[0] {
		case msgFramebufferUpdate:
			rects, err := readFramebufferUpdate(reader, img, nil)
			if err != nil {
				return nil, err
			}

			if rects > 0 {
				return img, nil
			}
		case msgSetColorMapEntries:
			if err := skipColorMap(reader); err != nil {
				return nil, err
			}
		case msgBell:
			// no payload
		case msgServerCutText:
			if err := skipCutText(reader); err != nil {
				return nil, err
			}
		default:
			return nil, failure(FailureProtocol, nil, "unexpected server message type %d", msgType[0])
		}
	}
}

// readFramebufferUpdate decodes one FramebufferUpdate (after its type byte)
// and returns the number of rectangles it carried. When dirty is not nil it
// accumulates the union of the painted areas.
func readFramebufferUpdate(reader io.Reader, img *image.RGBA, dirty *image.Rectangle) (int, error) {
	var header [3]byte // padding + number-of-rectangles
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return 0, fmt.Errorf("%w: %w", errNoFrame, err)
	}

	count := int(binary.BigEndian.Uint16(header[1:3]))
	bounds := img.Bounds()

	for range count {
		var rect struct {
			X, Y, W, H uint16
			Encoding   int32
		}
		if err := binary.Read(reader, binary.BigEndian, &rect); err != nil {
			return 0, fmt.Errorf("%w: %w", errNoFrame, err)
		}

		area := image.Rect(int(rect.X), int(rect.Y), int(rect.X)+int(rect.W), int(rect.Y)+int(rect.H))
		if !area.In(bounds) {
			return 0, failure(FailureProtocol, nil, "rectangle %v outside the %v framebuffer", area, bounds)
		}

		switch rect.Encoding {
		case encodingRaw:
			if err := decodeRaw(reader, img, area); err != nil {
				return 0, err
			}
		case encodingCopyRect:
			if err := decodeCopyRect(reader, img, area); err != nil {
				return 0, err
			}
		default:
			return 0, failure(FailureProtocol, nil, "unexpected rectangle encoding %d", rect.Encoding)
		}

		if dirty != nil {
			*dirty = dirty.Union(area)
		}
	}

	return count, nil
}

// decodeRaw copies a Raw rectangle: little-endian 32 bpp pixels, so each
// pixel arrives as B, G, R, X with the shifts requestFrame set.
func decodeRaw(reader io.Reader, img *image.RGBA, area image.Rectangle) error {
	row := make([]byte, area.Dx()*bytesPerPixel)

	for y := area.Min.Y; y < area.Max.Y; y++ {
		if _, err := io.ReadFull(reader, row); err != nil {
			return fmt.Errorf("%w: %w", errNoFrame, err)
		}

		offset := img.PixOffset(area.Min.X, y)
		for x := range area.Dx() {
			src := row[x*bytesPerPixel:]
			dst := img.Pix[offset+x*bytesPerPixel:]
			dst[0], dst[1], dst[2], dst[3] = src[2], src[1], src[0], 0xff
		}
	}

	return nil
}

// decodeCopyRect copies an already-decoded region of the framebuffer.
func decodeCopyRect(reader io.Reader, img *image.RGBA, area image.Rectangle) error {
	var src [2]uint16
	if err := binary.Read(reader, binary.BigEndian, &src); err != nil {
		return fmt.Errorf("%w: %w", errNoFrame, err)
	}

	srcRect := image.Rect(int(src[0]), int(src[1]), int(src[0])+area.Dx(), int(src[1])+area.Dy())
	if !srcRect.In(img.Bounds()) {
		return failure(FailureProtocol, nil, "copy source %v outside the framebuffer", srcRect)
	}

	// Snapshot the source first: source and destination may overlap.
	tmp := image.NewRGBA(srcRect)
	for y := srcRect.Min.Y; y < srcRect.Max.Y; y++ {
		copy(tmp.Pix[tmp.PixOffset(srcRect.Min.X, y):tmp.PixOffset(srcRect.Max.X-1, y)+bytesPerPixel],
			img.Pix[img.PixOffset(srcRect.Min.X, y):img.PixOffset(srcRect.Max.X-1, y)+bytesPerPixel])
	}

	for dy := range area.Dy() {
		dstOff := img.PixOffset(area.Min.X, area.Min.Y+dy)
		srcOff := tmp.PixOffset(srcRect.Min.X, srcRect.Min.Y+dy)
		copy(img.Pix[dstOff:dstOff+area.Dx()*bytesPerPixel], tmp.Pix[srcOff:srcOff+area.Dx()*bytesPerPixel])
	}

	return nil
}

// skipColorMap discards a SetColorMapEntries payload (after the type byte).
func skipColorMap(reader io.Reader) error {
	var header [5]byte // padding, first-color, number-of-colors
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return fmt.Errorf("%w: %w", errNoFrame, err)
	}

	n := int64(binary.BigEndian.Uint16(header[3:5])) * 6
	if _, err := io.CopyN(io.Discard, reader, n); err != nil {
		return fmt.Errorf("%w: %w", errNoFrame, err)
	}

	return nil
}

// skipCutText discards a ServerCutText payload (after the type byte).
func skipCutText(reader io.Reader) error {
	var header [7]byte // 3 padding + uint32 length
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return fmt.Errorf("%w: %w", errNoFrame, err)
	}

	n := binary.BigEndian.Uint32(header[3:7])
	if n > maxCutTextLen {
		return failure(FailureProtocol, nil, "server cut text too long (%d bytes)", n)
	}

	if _, err := io.CopyN(io.Discard, reader, int64(n)); err != nil {
		return fmt.Errorf("%w: %w", errNoFrame, err)
	}

	return nil
}

// encodePNG renders the captured frame.
func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}

	return buf.Bytes(), nil
}
