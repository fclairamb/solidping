package checkminecraft

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func motdPayload(motd string) []byte {
	body := make([]byte, 0, 35+len(motd))
	body = append(body, bedrockUnconnectedPongID)
	body = append(body, make([]byte, 8)...)
	body = append(body, make([]byte, 8)...)
	body = append(body, raknetMagic...)
	body = binary.BigEndian.AppendUint16(body, uint16(len(motd)))
	body = append(body, motd...)

	return body
}

// bedrockServer answers every datagram with reply (nothing when nil).
func bedrockServer(t *testing.T, reply []byte) int {
	t.Helper()

	pc, err := (&net.ListenConfig{}).ListenPacket(t.Context(), "udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = pc.Close() })

	go func() {
		buf := make([]byte, 2048)

		for {
			_, addr, rErr := pc.ReadFrom(buf)
			if rErr != nil {
				return
			}

			if reply != nil {
				_, _ = pc.WriteTo(reply, addr)
			}
		}
	}()

	port := pc.LocalAddr().(*net.UDPAddr).Port //nolint:forcetypeassert // udp listener

	return port
}

func TestParseBedrockPong(t *testing.T) {
	t.Parallel()

	good := "MCPE;My Server;594;1.20.0;3;10;123;Name;Survival"

	tests := []struct {
		name    string
		payload []byte
		wantErr error
	}{
		{name: "too short", payload: []byte{1, 2}, wantErr: errPongTooShort},
		{name: "wrong id", payload: append([]byte{0x05}, motdPayload(good)[1:]...), wantErr: errUnexpectedPongID},
		{name: "truncated string", payload: motdPayload(good)[:40], wantErr: errPongStringTruncate},
		{name: "malformed motd", payload: motdPayload("MCPE;x"), wantErr: errMalformedMOTD},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseBedrockPong(tt.payload)
			require.ErrorIs(t, err, tt.wantErr)
		})
	}

	status, err := parseBedrockPong(motdPayload(good))
	require.NoError(t, err)
	assert.Equal(t, &BedrockStatus{
		Edition: "MCPE", MOTD: "My Server", ProtocolVersion: 594, MinecraftVersion: "1.20.0",
		OnlinePlayers: 3, MaxPlayers: 10, ServerName: "123", GameMode: "Survival",
	}, status)

	short, err := parseBedrockMOTD("MCPE;m;1;v;2;4")
	require.NoError(t, err)
	assert.Empty(t, short.ServerName)
	assert.Empty(t, short.GameMode)
}

func TestExecuteBedrock(t *testing.T) {
	t.Parallel()

	port := bedrockServer(t, motdPayload("MCPE;My Server;594;1.20.0;3;10;123;Name;Survival"))
	checker := &MinecraftChecker{}

	tests := []struct {
		name   string
		cfg    *MinecraftConfig
		status checkerdef.Status
		errMsg string
	}{
		{
			name: "up", status: checkerdef.StatusUp,
			cfg: &MinecraftConfig{Host: "127.0.0.1", Port: port, Edition: EditionBedrock},
		},
		{
			name: "below minimum", status: checkerdef.StatusDown, errMsg: "player count below minimum",
			cfg: &MinecraftConfig{Host: "127.0.0.1", Port: port, Edition: EditionBedrock, MinPlayers: 5},
		},
		{
			name: "above maximum", status: checkerdef.StatusDown, errMsg: "player count above maximum",
			cfg: &MinecraftConfig{Host: "127.0.0.1", Port: port, Edition: EditionBedrock, MaxPlayers: 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := checker.Execute(t.Context(), tt.cfg)
			require.NoError(t, err)
			assert.Equal(t, tt.status, res.Status)

			if tt.errMsg != "" {
				assert.Equal(t, tt.errMsg, res.Output["error"])

				return
			}

			assert.Equal(t, "Survival", res.Output["gameMode"])
			assert.Equal(t, "My Server", res.Output["motd"])
			assert.Equal(t, 3, res.Metrics["players"])
			assert.Contains(t, res.Metrics, "total_time_ms")
		})
	}
}

func TestExecuteBedrockFailures(t *testing.T) {
	t.Parallel()

	checker := &MinecraftChecker{}

	t.Run("silent server times out", func(t *testing.T) {
		t.Parallel()

		port := bedrockServer(t, nil)
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		t.Cleanup(cancel)

		res, err := checker.Execute(ctx, &MinecraftConfig{
			Host: "127.0.0.1", Port: port, Edition: EditionBedrock, Timeout: time.Second,
		})
		require.NoError(t, err)
		assert.Equal(t, checkerdef.StatusTimeout, res.Status)
		assert.Equal(t, "query timeout", res.Output["error"])
	})

	t.Run("garbage reply is down", func(t *testing.T) {
		t.Parallel()

		port := bedrockServer(t, []byte{1, 2, 3})
		res, err := checker.Execute(t.Context(), &MinecraftConfig{Host: "127.0.0.1", Port: port, Edition: EditionBedrock})
		require.NoError(t, err)
		assert.Equal(t, checkerdef.StatusDown, res.Status)
		assert.Contains(t, res.Output["error"], "pong too short")
	})

	t.Run("unresolvable host", func(t *testing.T) {
		t.Parallel()

		_, err := bedrockUnconnectedPing(t.Context(), "invalid host name", 1, time.Second)
		require.ErrorContains(t, err, "dial")
	})

	t.Run("wrong config type", func(t *testing.T) {
		t.Parallel()

		_, err := checker.Execute(t.Context(), &wrongConfig{})
		require.Error(t, err)
	})
}

func writeVarInt(buf []byte, v int) []byte {
	for v >= 0x80 {
		buf = append(buf, byte(v&0x7f|0x80))
		v >>= 7
	}

	return append(buf, byte(v))
}

// javaServer speaks just enough of the 1.7 server list ping: it answers the
// status request with payload, then echoes the ping.
func javaServer(t *testing.T, payload any) int {
	t.Helper()

	ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, aErr := ln.Accept()
			if aErr != nil {
				return
			}

			go func() {
				defer func() { _ = conn.Close() }()

				_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
				buf := make([]byte, 512)
				// handshake + status request arrive together or apart: wait for the request.
				if _, rErr := conn.Read(buf); rErr != nil {
					return
				}

				raw, _ := json.Marshal(payload) //nolint:errchkjson // test fixture
				body := writeVarInt(nil, 0)
				body = writeVarInt(body, len(raw))
				body = append(body, raw...)
				frame := append(writeVarInt(nil, len(body)), body...)
				_, _ = conn.Write(frame)
				_, _ = io.Copy(io.Discard, conn)
			}()
		}
	}()

	port := ln.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // tcp listener

	return port
}

func TestExecuteJava(t *testing.T) {
	t.Parallel()

	checker := &MinecraftChecker{}

	t.Run("up with sample players", func(t *testing.T) {
		t.Parallel()

		port := javaServer(t, map[string]any{
			"version": map[string]any{"name": "1.20.4", "protocol": 765},
			"players": map[string]any{
				"max": 20, "online": 2,
				"sample": []map[string]any{{"name": "alice", "id": "069a79f4-44e9-4726-a5be-fca90e38aaf5"}},
			},
			"description": map[string]any{"text": "hello"},
		})

		res, err := checker.Execute(t.Context(), &MinecraftConfig{Host: "127.0.0.1", Port: port})
		require.NoError(t, err)
		require.Equal(t, checkerdef.StatusUp, res.Status, "%v", res.Output)
		assert.Equal(t, "1.20.4", res.Output["version"])
		assert.Equal(t, 2, res.Metrics["players"])
		assert.Equal(t, []string{"alice"}, res.Output["samplePlayers"])
	})

	t.Run("closed port is down", func(t *testing.T) {
		t.Parallel()

		ln, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
		require.NoError(t, err)

		port := ln.Addr().(*net.TCPAddr).Port //nolint:forcetypeassert // tcp listener
		require.NoError(t, ln.Close())

		res, err := checker.Execute(t.Context(), &MinecraftConfig{Host: "127.0.0.1", Port: port, Timeout: time.Second})
		require.NoError(t, err)
		assert.Equal(t, checkerdef.StatusDown, res.Status)
		assert.Contains(t, res.Output["error"], "minecraft query failed")
	})
}

type wrongConfig struct{}

func (*wrongConfig) FromMap(map[string]any) error { return nil }
func (*wrongConfig) GetConfig() map[string]any    { return nil }
func (*wrongConfig) Validate() error              { return nil }
