package checkmqtt_test

import (
	"net"
	"testing"
	"time"

	"github.com/eclipse/paho.mqtt.golang/packets"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkmqtt"
)

// fakeBroker is a minimal MQTT 3.1.1 broker. The flags make it misbehave at one
// precise step of the connect, subscribe, publish and echo sequence.
type fakeBroker struct {
	refuse      byte // CONNACK return code
	silentConn  bool // never answers CONNECT
	dropOnSub   bool // closes the connection on SUBSCRIBE
	silentSub   bool // never answers SUBSCRIBE
	silentPub   bool // never PUBACKs a PUBLISH
	noEcho      bool // acknowledges a PUBLISH but never delivers it
	wrongEcho   bool // delivers another payload
	dropOnPub   bool // closes the connection on PUBLISH
}

func (b *fakeBroker) start(t *testing.T) (string, int) {
	t.Helper()

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = lis.Close() })

	go func() {
		for {
			conn, acceptErr := lis.Accept()
			if acceptErr != nil {
				return
			}

			go b.serve(conn)
		}
	}()

	addr, ok := lis.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return addr.IP.String(), addr.Port
}

//nolint:cyclop,funlen // linear protocol script
func (b *fakeBroker) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()

	var topic string

	for {
		pkt, err := packets.ReadPacket(conn)
		if err != nil {
			return
		}

		switch p := pkt.(type) {
		case *packets.ConnectPacket:
			if b.silentConn {
				time.Sleep(3 * time.Second)

				return
			}

			ack, _ := packets.NewControlPacket(packets.Connack).(*packets.ConnackPacket)
			ack.ReturnCode = b.refuse
			_ = ack.Write(conn)
		case *packets.SubscribePacket:
			if b.dropOnSub {
				return
			}

			if b.silentSub {
				continue
			}

			topic = p.Topics[0]
			ack, _ := packets.NewControlPacket(packets.Suback).(*packets.SubackPacket)
			ack.MessageID = p.MessageID
			ack.ReturnCodes = []byte{1}
			_ = ack.Write(conn)
		case *packets.PublishPacket:
			if b.dropOnPub {
				return
			}

			if b.silentPub {
				continue
			}

			ack, _ := packets.NewControlPacket(packets.Puback).(*packets.PubackPacket)
			ack.MessageID = p.MessageID
			_ = ack.Write(conn)

			if b.noEcho {
				continue
			}

			out, _ := packets.NewControlPacket(packets.Publish).(*packets.PublishPacket)
			out.TopicName = topic
			out.Payload = p.Payload

			if b.wrongEcho {
				out.Payload = []byte("something else")
			}

			_ = out.Write(conn)
		case *packets.PingreqPacket:
			_ = packets.NewControlPacket(packets.Pingresp).Write(conn)
		case *packets.DisconnectPacket:
			return
		}
	}
}

func executeMQTT(t *testing.T, host string, port int, extra map[string]any) *checkerdef.Result {
	t.Helper()

	cfgMap := map[string]any{"host": host, "port": float64(port), "timeout": "1s"}
	for k, v := range extra {
		cfgMap[k] = v
	}

	config := &checkmqtt.MQTTConfig{}
	require.NoError(t, config.FromMap(cfgMap))

	result, err := (&checkmqtt.MQTTChecker{}).Execute(t.Context(), config)
	require.NoError(t, err)

	return result
}

func TestExecuteAgainstFakeBroker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		broker     *fakeBroker
		extra      map[string]any
		wantStatus checkerdef.Status
		wantError  string
	}{
		{name: "roundtrip works", broker: &fakeBroker{}, wantStatus: checkerdef.StatusUp},
		{
			name: "credentials are sent", broker: &fakeBroker{},
			extra:      map[string]any{"username": "alice", "password": "s3cret", "topic": "acme/health"},
			wantStatus: checkerdef.StatusUp,
		},
		{
			name: "connection refused by the broker", broker: &fakeBroker{refuse: 5},
			wantStatus: checkerdef.StatusDown, wantError: "connection failed",
		},
		{
			name: "no CONNACK", broker: &fakeBroker{silentConn: true},
			wantStatus: checkerdef.StatusTimeout, wantError: "connection timeout",
		},
		{
			name: "connection dropped on subscribe", broker: &fakeBroker{dropOnSub: true},
			wantStatus: checkerdef.StatusDown, wantError: "subscribe failed",
		},
		{
			name: "no SUBACK", broker: &fakeBroker{silentSub: true},
			wantStatus: checkerdef.StatusTimeout, wantError: "subscribe timeout",
		},
		{
			name: "no PUBACK", broker: &fakeBroker{silentPub: true},
			wantStatus: checkerdef.StatusTimeout, wantError: "publish timeout",
		},
		{
			name: "connection dropped on publish", broker: &fakeBroker{dropOnPub: true},
			wantStatus: checkerdef.StatusDown, wantError: "publish failed",
		},
		{
			name: "message never delivered", broker: &fakeBroker{noEcho: true},
			wantStatus: checkerdef.StatusTimeout, wantError: "message roundtrip timeout",
		},
		{
			name: "another message delivered", broker: &fakeBroker{wrongEcho: true},
			wantStatus: checkerdef.StatusTimeout, wantError: "message roundtrip timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			host, port := tt.broker.start(t)
			result := executeMQTT(t, host, port, tt.extra)

			require.Equal(t, tt.wantStatus, result.Status)

			if tt.wantError != "" {
				require.Contains(t, result.Output[checkerdef.OutputKeyError], tt.wantError)
			} else {
				require.Contains(t, result.Metrics, "roundtrip_time_ms")
				require.Contains(t, result.Metrics, "connection_time_ms")
			}
		})
	}
}

func TestExecuteSendsCredentialsAndTopic(t *testing.T) {
	t.Parallel()

	broker := &fakeBroker{}
	host, port := broker.start(t)

	result := executeMQTT(t, host, port, map[string]any{"username": "alice", "password": "s3cret", "topic": "acme/health"})
	require.Equal(t, checkerdef.StatusUp, result.Status)
	require.Equal(t, "acme/health", result.Output["topic"])
}
