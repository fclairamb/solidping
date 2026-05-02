package checkminecraft

import (
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

// bedrockUnconnectedPing sends a RakNet Unconnected Ping packet and parses the response.
func bedrockUnconnectedPing(host string, port int, timeout time.Duration) (*BedrockStatus, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))

	conn, err := net.DialTimeout("udp", addr, timeout)
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}

	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(timeout)
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("set deadline: %w", err)
	}

	packet := buildBedrockPing()
	if _, err := conn.Write(packet); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, 2048)

	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	return parseBedrockPong(buf[:n])
}

func buildBedrockPing() []byte {
	// Packet structure: ID (1) + timestamp (8) + magic (16) + clientGUID (8)
	packet := make([]byte, 0, 1+8+len(raknetMagic)+8)
	packet = append(packet, bedrockUnconnectedPingID)

	ts := make([]byte, 8)
	binary.BigEndian.PutUint64(ts, uint64(time.Now().UnixMilli())) //nolint:gosec // wraps fine

	packet = append(packet, ts...)
	packet = append(packet, raknetMagic...)

	guid := make([]byte, 8)
	binary.BigEndian.PutUint64(guid, 2)
	packet = append(packet, guid...)

	return packet
}

func parseBedrockPong(payload []byte) (*BedrockStatus, error) {
	if len(payload) < bedrockMinResponseLen {
		return nil, errors.New("pong too short")
	}

	if payload[0] != bedrockUnconnectedPongID {
		return nil, fmt.Errorf("unexpected packet id 0x%x", payload[0])
	}

	// Skip: id (1) + timestamp (8) + serverGUID (8) + magic (16) = 33 bytes
	// Next: uint16 length-prefixed string
	const headerLen = 33

	stringLen := int(binary.BigEndian.Uint16(payload[headerLen : headerLen+2]))
	start := headerLen + 2

	if len(payload) < start+stringLen {
		return nil, errors.New("pong string truncated")
	}

	motdString := string(payload[start : start+stringLen])

	return parseBedrockMOTD(motdString)
}

func parseBedrockMOTD(s string) (*BedrockStatus, error) {
	parts := strings.Split(s, ";")
	if len(parts) < bedrockMOTDFieldsMin {
		return nil, fmt.Errorf("malformed MOTD: %q", s)
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
