package checkkafka

import (
	"context"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

func TestKafkaTypeValidateSamples(t *testing.T) {
	t.Parallel()

	c := &KafkaChecker{}
	require.Equal(t, checkerdef.CheckTypeKafka, c.Type())
	require.NotEmpty(t, c.GetSampleConfigs(&checkerdef.ListSampleOptions{}))

	require.Error(t, c.Validate(&checkerdef.CheckSpec{
		Name: "k", Slug: "k", Period: time.Minute, Config: (&KafkaConfig{}).GetConfig(),
	}))
	require.NoError(t, c.Validate(&checkerdef.CheckSpec{
		Name: "k", Slug: "k", Period: time.Minute,
		Config: (&KafkaConfig{Brokers: []string{"localhost:9092"}}).GetConfig(),
	}))
}

func TestBuildSaramaConfig(t *testing.T) {
	t.Parallel()

	c := &KafkaChecker{}
	tests := []struct {
		name string
		cfg  KafkaConfig
		sasl bool
		mech sarama.SASLMechanism
		tls  bool
	}{
		{"plain no auth", KafkaConfig{}, false, "", false},
		{"tls", KafkaConfig{TLS: true, TLSSkipVerify: true}, false, "", true},
		{
			"PLAIN",
			KafkaConfig{SASLMechanism: "PLAIN", SASLUsername: "u", SASLPassword: "p"},
			true, sarama.SASLTypePlaintext, false,
		},
		{"SCRAM256", KafkaConfig{SASLMechanism: "SCRAM-SHA-256"}, true, sarama.SASLTypeSCRAMSHA256, false},
		{"SCRAM512", KafkaConfig{SASLMechanism: "SCRAM-SHA-512"}, true, sarama.SASLTypeSCRAMSHA512, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			sc := c.buildSaramaConfig(&tt.cfg, time.Second)
			require.Equal(t, tt.sasl, sc.Net.SASL.Enable)
			require.Equal(t, tt.mech, sc.Net.SASL.Mechanism)
			require.Equal(t, tt.tls, sc.Net.TLS.Enable)

			if sc.Net.SASL.SCRAMClientGeneratorFunc != nil {
				cl := sc.Net.SASL.SCRAMClientGeneratorFunc()
				require.NoError(t, cl.Begin("user", "pass", ""))

				first, err := cl.Step("")
				require.NoError(t, err)
				require.NotEmpty(t, first)
				require.False(t, cl.Done())

				// A garbage challenge must fail rather than panic.
				_, err = cl.Step("garbage")
				require.Error(t, err)
			}
		})
	}
}

func TestDurationMs(t *testing.T) {
	t.Parallel()
	require.InDelta(t, 1.5, durationMs(1500*time.Microsecond), 0.0001)
}

func TestHandleConnectError(t *testing.T) {
	t.Parallel()

	res := handleConnectError(context.Background(), context.DeadlineExceeded, time.Now())
	require.Equal(t, checkerdef.StatusDown, res.Status)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res = handleConnectError(ctx, context.Canceled, time.Now())
	require.Equal(t, checkerdef.StatusTimeout, res.Status)
}

func TestExecuteConfigAssertion(t *testing.T) {
	t.Parallel()

	_, err := (&KafkaChecker{}).Execute(context.Background(), nil)
	require.Error(t, err)
}

func TestExecuteUnreachableBroker(t *testing.T) {
	t.Parallel()

	res, err := (&KafkaChecker{}).Execute(context.Background(), &KafkaConfig{
		Brokers: []string{"127.0.0.1:1"}, Timeout: time.Second,
	})
	require.NoError(t, err)
	require.Contains(t, []checkerdef.Status{checkerdef.StatusDown, checkerdef.StatusTimeout}, res.Status)
}

// newMockBroker returns a mock broker answering metadata for the given topics.
func newMockBroker(t *testing.T, topics ...string) *sarama.MockBroker {
	t.Helper()

	broker := sarama.NewMockBroker(t, 1)
	t.Cleanup(broker.Close)

	meta := sarama.NewMockMetadataResponse(t).
		SetBroker(broker.Addr(), broker.BrokerID()).
		SetController(broker.BrokerID())

	for _, topic := range topics {
		meta.SetLeader(topic, 0, broker.BrokerID())
	}

	broker.SetHandlerByMap(map[string]sarama.MockResponse{
		"MetadataRequest": meta,
		"ProduceRequest": sarama.NewMockProduceResponse(t).
			SetError("events", 0, sarama.ErrNoError),
	})

	return broker
}

func TestExecuteWithMockBroker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		topic  string
		produc bool
		want   checkerdef.Status
		errKey string
	}{
		{"cluster only", "", false, checkerdef.StatusUp, ""},
		{"topic exists", "events", false, checkerdef.StatusUp, ""},
		{"topic missing", "nope", false, checkerdef.StatusDown, "error"},
		{"produce", "events", true, checkerdef.StatusUp, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			broker := newMockBroker(t, "events")

			res, err := (&KafkaChecker{}).Execute(context.Background(), &KafkaConfig{
				Brokers: []string{broker.Addr()}, Topic: tt.topic, ProduceTest: tt.produc, Timeout: time.Second,
			})
			require.NoError(t, err)
			require.Equal(t, tt.want, res.Status, "%v", res.Output)

			if tt.errKey != "" {
				require.Contains(t, res.Output, tt.errKey)
			}

			if tt.produc {
				require.Contains(t, res.Output, "produceOffset")
			}
		})
	}
}
