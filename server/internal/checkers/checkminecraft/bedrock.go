package checkminecraft

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// BedrockStatus contains the parsed response from a Bedrock unconnected ping.
type BedrockStatus struct {
	Edition          string
	MOTD             string
	ProtocolVersion  int
	MinecraftVersion string
	OnlinePlayers    int
	MaxPlayers       int
	ServerName       string
	GameMode         string
}

// RakNet "magic" sequence used by the Unconnected Ping packet (16 bytes).
//
//nolint:gochecknoglobals // RakNet protocol constant.
var raknetMagic = []byte{
	0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe,
	0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78,
}

const (
	bedrockUnconnectedPingID  byte = 0x01
	bedrockUnconnectedPongID  byte = 0x1c
	bedrockMinResponseLen          = 35
	bedrockMOTDFieldsMin           = 6
	bedrockMOTDFieldsExtended      = 9
)

var (
	errPongTooShort       = errors.New("pong too short")
	errUnexpectedPongID   = errors.New("unexpected packet id")
	errPongStringTruncate = errors.New("pong string truncated")
	errMalformedMOTD      = errors.New("malformed MOTD")
)

// bedrockUnconnectedPing sends a RakNet Unconnected Ping packet and parses the response.
func bedrockUnconnectedPing(
	ctx context.Context,
	host string,
	port int,
	timeout time.Duration,
) (*BedrockStatus, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	dialer := &net.Dialer{Timeout: timeout}

	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	conn, err := dialer.DialContext(dialCtx, "udp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	defer func() { _ = conn.Close() }()

	if dlErr := conn.SetDeadline(time.Now().Add(timeout)); dlErr != nil {
		return nil, fmt.Errorf("set deadline: %w", dlErr)
	}

	packet := buildBedrockPing()
	if _, wErr := conn.Write(packet); wErr != nil {
		return nil, fmt.Errorf("write: %w", wErr)
	}

	buf := make([]byte, 2048)

	n, rErr := conn.Read(buf)
	if rErr != nil {
		return nil, fmt.Errorf("read: %w", rErr)
	}

	return parseBedrockPong(buf[:n])
}

func buildBedrockPing() []byte {
	// Packet structure: ID (1) + timestamp (8) + magic (16) + clientGUID (8)
	packet := make([]byte, 0, 1+8+len(raknetMagic)+8)
	packet = append(packet, bedrockUnconnectedPingID)

	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, uint64(time.Now().UnixMilli()))

	packet = append(packet, ts...)
	packet = append(packet, raknetMagic...)

	guid := make([]byte, 8)
	binary.BigEndian.PutUint64(guid, 2)
	packet = append(packet, guid...)

	return packet
}

func parseBedrockPong(payload []byte) (*BedrockStatus, error) {
	if len(payload) < bedrockMinResponseLen {
		return nil, errPongTooShort
	}

	if payload[0] != bedrockUnconnectedPongID {
		return nil, fmt.Errorf("%w: 0x%x", errUnexpectedPongID, payload[0])
	}

	// Skip: id (1) + timestamp (8) + serverGUID (8) + magic (16) = 33 bytes
	// Next: uint16 length-prefixed string
	const headerLen = 33

	stringLen := int(binary.BigEndian.Uint16(payload[headerLen : headerLen+2]))
	start := headerLen + 2

	if len(payload) < start+stringLen {
		return nil, errPongStringTruncate
	}

	motdString := string(payload[start : start+stringLen])

	return parseBedrockMOTD(motdString)
}

func parseBedrockMOTD(s string) (*BedrockStatus, error) {
	parts := strings.Split(s, ";")
	if len(parts) < bedrockMOTDFieldsMin {
		return nil, fmt.Errorf("%w: %q", errMalformedMOTD, s)
	}

	protocol, _ := strconv.Atoi(parts[2])
	online, _ := strconv.Atoi(parts[4])
	maxPlayers, _ := strconv.Atoi(parts[5])

	status := &BedrockStatus{
		Edition:          parts[0],
		MOTD:             parts[1],
		ProtocolVersion:  protocol,
		MinecraftVersion: parts[3],
		OnlinePlayers:    online,
		MaxPlayers:       maxPlayers,
	}

	if len(parts) >= 7 {
		status.ServerName = parts[6]
	}

	if len(parts) >= bedrockMOTDFieldsExtended {
		status.GameMode = parts[8]
	}

	return status, nil
}
