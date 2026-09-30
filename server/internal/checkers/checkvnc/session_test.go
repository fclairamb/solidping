package checkvnc

import (
	"bytes"
	"context"
	"crypto/des" //nolint:gosec // reference computation for the VNC-auth vector
	"encoding/hex"
	"errors"
	"fmt"
	"image/color"
	"image/png"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// fixedChallenge is 00 01 .. 0f.
func fixedChallenge() []byte {
	c := make([]byte, challengeLen)
	for i := range c {
		c[i] = byte(i)
	}

	return c
}

// TestVNCAuthKnownVector pins the DES response for password "password" and
// challenge 00..0f. The expected bytes were computed independently with
// `openssl enc -des-ecb -K 0e86ceceeef64e26 -nopad`, the key being
// "password" with each byte bit-reversed. Dropping the bit reversal gives a
// different answer (eb175c5f...), so this test fails if it goes missing.
func TestVNCAuthKnownVector(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	got, err := vncAuthResponse("password", fixedChallenge())
	r.NoError(err)
	r.Equal("b866924125c8eebb9debc1db61c538e2", hex.EncodeToString(got))
	r.Equal("0e86ceceeef64e26", hex.EncodeToString(vncAuthKey("password")))
}

// TestVNCAuthBitReversalMatters shows the vector above discriminates: the
// same DES with the raw (non-reversed) key gives the other answer.
func TestVNCAuthBitReversalMatters(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	block, err := des.NewCipher([]byte("password"))
	r.NoError(err)

	unreversed := make([]byte, challengeLen)
	block.Encrypt(unreversed[:8], fixedChallenge()[:8])
	block.Encrypt(unreversed[8:], fixedChallenge()[8:])
	r.Equal("eb175c5f075811cd27669005286b11cc", hex.EncodeToString(unreversed))

	got, err := vncAuthResponse("password", fixedChallenge())
	r.NoError(err)
	r.NotEqual(unreversed, got)

	r.Equal(byte(0x80), reverseBits(0x01))
	r.Equal(byte(0x0e), reverseBits('p'))
}

// TestVNCAuthKeyTruncationAndPadding: only 8 password bytes count; shorter
// passwords are zero-padded.
func TestVNCAuthKeyTruncationAndPadding(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	r.Equal(vncAuthKey("password"), vncAuthKey("password-and-more"))
	r.Equal([]byte{reverseBits('a'), 0, 0, 0, 0, 0, 0, 0}, vncAuthKey("a"))
}

// vncAuthServer scripts a 3.8 server offering types, running VNC auth and
// answering with result (and reason on failure). When init is non-nil the
// server continues with ClientInit/ServerInit and hands over to after.
type vncAuthServer struct {
	types    []byte
	password string
	result   uint32
	reason   string
	// sharedFlag receives the ClientInit byte.
	sharedFlag chan byte
	init       []byte
	after      func(conn net.Conn) error
}

func (s *vncAuthServer) run(conn net.Conn) error {
	if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
		return err
	}

	if _, err := readN(conn, bannerLen); err != nil {
		return err
	}

	if err := writeAll(conn, []byte{byte(len(s.types))}, s.types); err != nil {
		return err
	}

	chosen, err := readN(conn, 1)
	if err != nil {
		return err
	}

	if chosen[0] != secVNCAuth {
		return fmt.Errorf("client chose %d, want VNC auth", chosen[0])
	}

	if err := writeAll(conn, fixedChallenge()); err != nil {
		return err
	}

	response, err := readN(conn, challengeLen)
	if err != nil {
		return err
	}

	want, _ := vncAuthResponse(s.password, fixedChallenge())
	if s.result == 0 && !bytes.Equal(response, want) {
		return errors.New("client sent a wrong challenge response")
	}

	if s.result != 0 {
		return writeAll(conn, u32(s.result), reasonBytes(s.reason))
	}

	if err := writeAll(conn, u32(0)); err != nil {
		return err
	}

	flag, err := readN(conn, 1)
	if err != nil {
		return err
	}

	if s.sharedFlag != nil {
		s.sharedFlag <- flag[0]
	}

	if err := writeAll(conn, s.init); err != nil {
		return err
	}

	if s.after != nil {
		return s.after(conn)
	}

	return nil
}

// TestWrongPassword: SecurityResult=1 with a reason is AUTH_FAILED carrying it.
func TestWrongPassword(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vncAuthServer{types: []byte{secVNCAuth}, result: 1, reason: "Authentication failed"}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "wrong"})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(string(FailureAuthFailed), result.Output["failure_code"])
	r.Contains(result.Output["error"], "Authentication failed")
}

// TestTooManyAttempts: the TightVNC lockout reason maps to its own code.
func TestTooManyAttempts(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vncAuthServer{types: []byte{secVNCAuth}, result: 1, reason: "Too many authentication failures"}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "wrong"})
	r.NoError(serverErr(t, errs))
	r.Equal(string(FailureTooManyAttempts), result.Output["failure_code"])
}

// TestAuthenticatedUpSharedFlag: the right password logs in, the ClientInit
// byte the server receives is shared-flag = 1, and ServerInit lands in the
// output.
func TestAuthenticatedUpSharedFlag(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	shared := make(chan byte, 1)
	srv := &vncAuthServer{
		types: []byte{secVNCAuth}, password: "s3cret", sharedFlag: shared,
		init: serverInitBytes(1024, 768, "acme desktop"),
	}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "s3cret"})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal(byte(1), <-shared, "ClientInit must set the shared flag")
	r.Equal("acme desktop", result.Output["desktopName"])
	r.Equal(1024, result.Output["width"])
	r.Equal(768, result.Output["height"])
	r.Contains(result.Metrics, "auth_ms")
	r.Nil(result.Diagnostics, "no screenshot requested")
}

// frameServer answers the frame request of a 4x2 desktop: a Bell, a
// ServerCutText, then one update with a Raw 2x2 rectangle at (0,0) and a
// CopyRect of it to (2,0). It checks the client's request messages.
func frameServer(conn net.Conn) error {
	// SetPixelFormat(20) + SetEncodings(4 + 2*4) + FramebufferUpdateRequest(10)
	req, err := readN(conn, 20+12+10)
	if err != nil {
		return err
	}

	if req[0] != msgSetPixelFormat || req[4] != 32 || req[7] != 1 {
		return fmt.Errorf("bad SetPixelFormat %x", req[:20])
	}

	if req[20] != msgSetEncodings || req[23] != 2 {
		return fmt.Errorf("bad SetEncodings %x", req[20:32])
	}

	fbur := req[32:]
	if fbur[0] != msgFramebufferUpdateRequest || fbur[1] != 0 || !bytes.Equal(fbur[6:10], []byte{0, 4, 0, 2}) {
		return fmt.Errorf("bad FramebufferUpdateRequest %x", fbur)
	}

	// Pixels as B, G, R, X: red, green / blue, white.
	raw := []byte{
		0, 0, 255, 0, 0, 255, 0, 0,
		255, 0, 0, 0, 255, 255, 255, 0,
	}

	update := []byte{msgFramebufferUpdate, 0}
	update = append(update, u16(2)...)
	update = append(update, u16(0)...)
	update = append(update, u16(0)...)
	update = append(update, u16(2)...)
	update = append(update, u16(2)...)
	update = append(update, u32(uint32(encodingRaw))...)
	update = append(update, raw...)
	update = append(update, u16(2)...)
	update = append(update, u16(0)...)
	update = append(update, u16(2)...)
	update = append(update, u16(2)...)
	update = append(update, u32(uint32(encodingCopyRect))...)
	update = append(update, u16(0)...)
	update = append(update, u16(0)...)

	cut := append([]byte{msgServerCutText, 0, 0, 0}, reasonBytes("clipboard")...)

	return writeAll(conn, []byte{msgBell}, cut, update)
}

// TestScreenshotRawAndCopyRect: the decoded PNG has the ServerInit size and
// the expected pixels, CopyRect included.
func TestScreenshotRawAndCopyRect(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vncAuthServer{
		types: []byte{secVNCAuth}, password: "pw", init: serverInitBytes(4, 2, "d"), after: frameServer,
	}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "pw", Screenshot: true})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.NotNil(result.Diagnostics)
	r.NotNil(result.Diagnostics.Screenshot, result.Diagnostics.ScreenshotError)
	r.Contains(result.Metrics, "first_frame_ms")

	img, err := png.Decode(bytes.NewReader(result.Diagnostics.Screenshot.Image))
	r.NoError(err)
	r.Equal(4, img.Bounds().Dx())
	r.Equal(2, img.Bounds().Dy())

	red := color.RGBA{255, 0, 0, 255}
	green := color.RGBA{0, 255, 0, 255}
	blue := color.RGBA{0, 0, 255, 255}
	white := color.RGBA{255, 255, 255, 255}

	for _, px := range []struct {
		x, y int
		want color.RGBA
	}{
		{0, 0, red}, {1, 0, green}, {0, 1, blue}, {1, 1, white},
		{2, 0, red}, {3, 0, green}, {2, 1, blue}, {3, 1, white},
	} {
		r.Equal(px.want, color.RGBAModel.Convert(img.At(px.x, px.y)), "pixel (%d,%d)", px.x, px.y)
	}
}

// TestScreenshotUnexpectedEncoding fails cleanly on an encoding we did not ask for.
func TestScreenshotUnexpectedEncoding(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vncAuthServer{
		types: []byte{secVNCAuth}, password: "pw", init: serverInitBytes(4, 2, "d"),
		after: func(conn net.Conn) error {
			if _, err := readN(conn, 42); err != nil {
				return err
			}

			update := []byte{msgFramebufferUpdate, 0}
			update = append(update, u16(1)...)
			update = append(update, u16(0)...)
			update = append(update, u16(0)...)
			update = append(update, u16(4)...)
			update = append(update, u16(2)...)
			update = append(update, u32(7)...) // Tight

			return writeAll(conn, update)
		},
	}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "pw", Screenshot: true})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(string(FailureProtocol), result.Output["failure_code"])
	r.Contains(result.Output["error"], "encoding 7")
}

// TestScreenshotNoFrame: a server that never draws is down NO_FRAME, not a
// hang.
func TestScreenshotNoFrame(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	srv := &vncAuthServer{
		types: []byte{secVNCAuth}, password: "pw", init: serverInitBytes(4, 2, "d"),
		after: func(conn net.Conn) error {
			if _, err := readN(conn, 42); err != nil {
				return err
			}

			// Hold the connection open until the client gives up.
			_, _ = readN(conn, 1)

			return nil
		},
	}
	host, port, errs := fakeServer(t, srv.run)

	result := runCheck(t, &VNCConfig{
		Host: host, Port: port, Password: "pw", Screenshot: true, Timeout: 500 * time.Millisecond,
	})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusDown, result.Status)
	r.Equal(string(FailureNoFrame), result.Output["failure_code"])
	r.NotNil(result.Diagnostics)
	r.NotEmpty(result.Diagnostics.ScreenshotError)
}

// TestAuthTypeUnsupported: only VeNCrypt or only ARD offered, with a
// password configured, is AUTH_TYPE_UNSUPPORTED listing what was offered.
func TestAuthTypeUnsupported(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		types []byte
		want  string
	}{
		{"vencrypt", []byte{secVeNCrypt}, "VeNCrypt (19)"},
		{"ard", []byte{secARD}, "Apple Remote Desktop (30)"},
		{"both", []byte{secVeNCrypt, secARD}, "VeNCrypt (19), Apple Remote Desktop (30)"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			host, port, errs := fakeServer(t, func(conn net.Conn) error {
				if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
					return err
				}

				if _, err := readN(conn, bannerLen); err != nil {
					return err
				}

				return writeAll(conn, []byte{byte(len(tc.types))}, tc.types)
			})

			result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "pw"})
			r.NoError(serverErr(t, errs))
			r.Equal(checkerdef.StatusDown, result.Status)
			r.Equal(string(FailureAuthUnsupported), result.Output["failure_code"])
			r.Contains(result.Output["error"], tc.want)

			// Positive control: without a password the same server is a
			// healthy handshake (the audit alone does not need type 2).
			host2, port2, errs2 := fakeServer(t, func(conn net.Conn) error {
				if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
					return err
				}

				if _, err := readN(conn, bannerLen); err != nil {
					return err
				}

				return writeAll(conn, []byte{byte(len(tc.types))}, tc.types)
			})

			up := runCheck(t, &VNCConfig{Host: host2, Port: port2})
			r.NoError(serverErr(t, errs2))
			r.Equal(checkerdef.StatusUp, up.Status)
		})
	}
}

// TestNoneAuthWhenAllowed: requireAuth false + only None + a password goes
// through None (3.8 sends a SecurityResult) and reaches ServerInit.
func TestNoneAuthWhenAllowed(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	host, port, errs := fakeServer(t, func(conn net.Conn) error {
		if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
			return err
		}

		if _, err := readN(conn, bannerLen); err != nil {
			return err
		}

		if err := writeAll(conn, []byte{1, secNone}); err != nil {
			return err
		}

		if _, err := readN(conn, 1); err != nil {
			return err
		}

		if err := writeAll(conn, u32(0)); err != nil {
			return err
		}

		if _, err := readN(conn, 1); err != nil {
			return err
		}

		return writeAll(conn, serverInitBytes(800, 600, "open"))
	})

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "pw", RequireAuth: boolPtr(false)})
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusUp, result.Status, result.Output)
	r.Equal("None", result.Output["securityType"])
	r.Equal("open", result.Output["desktopName"])
}

// TestVNCAuth33: a 3.3 server picks VNC auth itself (no type byte from the
// client) and sends no reason on failure.
func TestVNCAuth33(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	host, port, errs := fakeServer(t, func(conn net.Conn) error {
		if err := writeAll(conn, []byte("RFB 003.003\n"), nil); err != nil {
			return err
		}

		if _, err := readN(conn, bannerLen); err != nil {
			return err
		}

		if err := writeAll(conn, u32(uint32(secVNCAuth)), fixedChallenge()); err != nil {
			return err
		}

		if _, err := readN(conn, challengeLen); err != nil {
			return err
		}

		return writeAll(conn, u32(1))
	})

	result := runCheck(t, &VNCConfig{Host: host, Port: port, Password: "pw"})
	r.NoError(serverErr(t, errs))
	r.Equal(string(FailureAuthFailed), result.Output["failure_code"])
}

// TestForcedCaptureWithoutPassword: "Capture now" on a handshake-only check
// says why no image came back instead of staying silent.
func TestForcedCaptureWithoutPassword(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	host, port, errs := fakeServer(t, func(conn net.Conn) error {
		if err := writeAll(conn, []byte("RFB 003.008\n")); err != nil {
			return err
		}

		if _, err := readN(conn, bannerLen); err != nil {
			return err
		}

		return writeAll(conn, []byte{1, secVNCAuth})
	})

	ctx := checkerdef.WithForcedCapture(context.Background())
	result, err := (&VNCChecker{}).Execute(ctx, &VNCConfig{Host: host, Port: port, Timeout: 3 * time.Second})
	r.NoError(err)
	r.NoError(serverErr(t, errs))
	r.Equal(checkerdef.StatusUp, result.Status)
	r.NotNil(result.Diagnostics)
	r.Contains(result.Diagnostics.ScreenshotError, "password")
}
