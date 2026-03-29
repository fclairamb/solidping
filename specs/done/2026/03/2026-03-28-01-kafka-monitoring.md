# Kafka Broker Monitoring

## Overview

Add a Kafka health check that verifies a Kafka cluster is reachable and operational. The check connects to the broker, retrieves cluster metadata, and optionally produces a test message to validate end-to-end write capability. This is critical for event-driven architectures where Kafka availability directly impacts business processes.

**Use cases:**
- Verify Kafka brokers are reachable and accepting connections
- Validate cluster metadata (broker count, topic availability)
- Test producer capability by writing a message to a health check topic
- Monitor connection latency to the Kafka cluster
- Detect split-brain scenarios by checking broker count against expected value
- Validate SASL/TLS authentication is working

## Check Type
Type: `kafka`

---

## Backend

### Package: `server/internal/checkers/checkkafka/`

| File | Description |
|------|-------------|
| `config.go` | `KafkaConfig` struct with `FromMap()` / `GetConfig()` |
| `checker.go` | `KafkaChecker` implementing `Checker` interface |
| `errors.go` | Package-level error sentinel values |
| `samples.go` | `GetSampleConfigs()` with example configurations |
| `checker_test.go` | Table-driven tests with testcontainers |

### Configuration (`KafkaConfig`)

```go
type KafkaConfig struct {
    Brokers       []string      `json:"brokers"`
    Topic         string        `json:"topic,omitempty"`
    SASLMechanism string        `json:"sasl_mechanism,omitempty"`
    SASLUsername  string        `json:"sasl_username,omitempty"`
    SASLPassword  string        `json:"sasl_password,omitempty"`
    TLS           bool          `json:"tls,omitempty"`
    TLSSkipVerify bool          `json:"tls_skip_verify,omitempty"`
    Timeout       time.Duration `json:"timeout,omitempty"`
    ProduceTest   bool          `json:"produce_test,omitempty"`
}
```

| Field | Type | Required | Default | Description |
|-------|------|----------|---------|-------------|
| `brokers` | string[] | **yes** | — | Comma-separated list of broker addresses (`host:port`) |
| `topic` | string | no | — | Topic to check existence of (or produce test message to) |
| `sasl_mechanism` | string | no | — | SASL mechanism: `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512` |
| `sasl_username` | string | no | — | SASL username |
| `sasl_password` | string | no | — | SASL password |
| `tls` | bool | no | `false` | Enable TLS |
| `tls_skip_verify` | bool | no | `false` | Skip TLS certificate verification |
| `timeout` | duration | no | `10s` | Connection timeout |
| `produce_test` | bool | no | `false` | If true, produce a test message to the topic |

### Validation Rules

- `brokers` must contain at least one broker address
- Each broker must be in `host:port` format
- `sasl_mechanism` if provided must be one of: `PLAIN`, `SCRAM-SHA-256`, `SCRAM-SHA-512`
- If `sasl_mechanism` is set, both `sasl_username` and `sasl_password` are required
- `produce_test` requires `topic` to be set
- `timeout` must be > 0 and ≤ 60s
- Auto-generate `spec.Name` from first broker address if empty
- Auto-generate `spec.Slug` as `kafka-{first-broker-host}` if empty

### Execution Behavior

**Metadata-only mode** (default):
1. Create Sarama client config with timeout, SASL, and TLS settings
2. Record `t0` — connect to cluster via `sarama.NewClient(brokers, config)`
3. Record `t1` — compute `connection_time_ms`
4. Fetch cluster metadata: broker list, controller ID
5. If `topic` is set, verify topic exists in metadata
6. Close client
7. Return `StatusUp` with broker count and topic info

**Producer mode** (`produce_test: true`):
1. Steps 1-4 above
2. Create sync producer from client
3. Send a timestamped test message to the configured topic
4. Record `t2` — compute `produce_time_ms`
5. Close producer and client
6. Return `StatusUp` with partition/offset info

**Status mapping:**

| Condition | Status |
|-----------|--------|
| Metadata fetch succeeds (+ topic exists if specified) | `StatusUp` |
| Test message produced successfully | `StatusUp` |
| All brokers unreachable | `StatusDown` |
| SASL authentication failure | `StatusDown` |
| TLS handshake failure | `StatusDown` |
| Topic not found (when topic specified) | `StatusDown` |
| Producer send failure | `StatusDown` |
| Context deadline exceeded | `StatusTimeout` |
| Invalid configuration | `StatusError` |

### Metrics

| Key | Type | Description |
|-----|------|-------------|
| `connection_time_ms` | float64 | Time to connect and fetch metadata |
| `broker_count` | int | Number of brokers in cluster |
| `produce_time_ms` | float64 | Time to produce test message (if enabled) |
| `total_time_ms` | float64 | Total check duration |

### Output

| Key | Type | Description |
|-----|------|-------------|
| `brokers` | string | Connected broker addresses |
| `controller_id` | int | Cluster controller broker ID |
| `broker_count` | int | Number of brokers in cluster |
| `topic_exists` | bool | Whether the specified topic exists |
| `produce_partition` | int | Partition test message was sent to |
| `produce_offset` | int | Offset of test message |
| `error` | string | Error message if check failed |

### Go Driver

Use `github.com/IBM/sarama` (most popular Go Kafka client, formerly Shopify/sarama). Add to `server/go.mod`.

```go
import "github.com/IBM/sarama"

func buildSaramaConfig(cfg *KafkaConfig) *sarama.Config {
    sc := sarama.NewConfig()
    sc.Net.DialTimeout = cfg.resolveTimeout()
    sc.Net.ReadTimeout = cfg.resolveTimeout()
    sc.Net.WriteTimeout = cfg.resolveTimeout()

    if cfg.TLS {
        sc.Net.TLS.Enable = true
        sc.Net.TLS.Config = &tls.Config{InsecureSkipVerify: cfg.TLSSkipVerify}
    }

    if cfg.SASLMechanism != "" {
        sc.Net.SASL.Enable = true
        sc.Net.SASL.Mechanism = sarama.SASLMechanism(cfg.SASLMechanism)
        sc.Net.SASL.User = cfg.SASLUsername
        sc.Net.SASL.Password = cfg.SASLPassword
        if cfg.SASLMechanism == "SCRAM-SHA-256" {
            sc.Net.SASL.SCRAMClientGeneratorFunc = func() sarama.SCRAMClient { ... }
        }
    }

    if cfg.ProduceTest {
        sc.Producer.Return.Successes = true
    }

    return sc
}
```

### Testing

Use **testcontainers** with Kafka (e.g., `confluentinc/cp-kafka` or `bitnami/kafka`):

**Test cases** (table-driven):
1. **Happy path** — metadata fetch succeeds, expect `StatusUp`
2. **Broker count** — verify broker count in metrics
3. **Topic exists** — specify existing topic, expect `StatusUp`
4. **Topic not found** — specify non-existent topic, expect `StatusDown`
5. **Produce test message** — produce to topic, expect `StatusUp` with offset
6. **All brokers down** — wrong addresses, expect `StatusDown`
7. **SASL auth failure** — wrong credentials, expect `StatusDown`
8. **Timeout** — unreachable broker, expect `StatusTimeout`
9. **Missing brokers** — validation error
10. **Produce without topic** — validation error

### Limitations

- Consumer lag monitoring not supported (requires consumer group tracking)
- Schema Registry health not checked
- Kafka Connect health not checked
- Only sync producer for test messages (adds latency)
- SASL/OAUTHBEARER not supported in initial implementation
- No ACL validation

---

## Frontend

### Form Fields

```tsx
case "kafka":
  return (
    <>
      <div className="space-y-2">
        <Label htmlFor="brokers">Brokers</Label>
        <Input id="brokers" type="text" placeholder="broker1:9092,broker2:9092"
          value={brokers} onChange={(e) => setBrokers(e.target.value)} />
        <p className="text-xs text-muted-foreground">Comma-separated list of broker addresses</p>
      </div>
      <div className="space-y-2">
        <Label htmlFor="topic">Topic (optional)</Label>
        <Input id="topic" type="text" placeholder="my-topic"
          value={topic} onChange={(e) => setTopic(e.target.value)} />
      </div>
      <div className="flex gap-4">
        <div className="space-y-2 flex-1">
          <Label htmlFor="username">SASL Username (optional)</Label>
          <Input id="username" type="text" value={username} onChange={(e) => setUsername(e.target.value)} />
        </div>
        <div className="space-y-2 flex-1">
          <Label htmlFor="password">SASL Password (optional)</Label>
          <Input id="password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
      </div>
      <div className="space-y-3">
        <label className="flex items-center gap-2">
          <Checkbox checked={tls} onCheckedChange={(v) => setTls(v === true)} />
          <span className="text-sm">Use TLS</span>
        </label>
        <label className="flex items-center gap-2">
          <Checkbox checked={produceTest} onCheckedChange={(v) => setProduceTest(v === true)} />
          <span className="text-sm">Produce test message</span>
        </label>
      </div>
    </>
  );
```

---

## Key Files

| File | Action |
|------|--------|
| `server/internal/checkers/checkkafka/config.go` | New |
| `server/internal/checkers/checkkafka/checker.go` | New |
| `server/internal/checkers/checkkafka/errors.go` | New |
| `server/internal/checkers/checkkafka/samples.go` | New |
| `server/internal/checkers/checkkafka/checker_test.go` | New |
| `server/internal/checkers/checkerdef/types.go` | Modify |
| `server/internal/checkers/registry/registry.go` | Modify |
| `server/go.mod` | Modify (add `github.com/IBM/sarama`) |
| `web/dash0/src/components/shared/check-form.tsx` | Modify |
| `web/dash0/src/locales/en/checks.json` | Modify |
| `web/dash0/src/locales/fr/checks.json` | Modify |

## Verification

- [ ] `make lint` passes
- [ ] `make test` passes
- [ ] Create a Kafka check via the UI
- [ ] Verify metadata fetch shows broker count
- [ ] Verify topic existence check works
- [ ] Verify test message production works
- [ ] Verify SASL authentication works
- [ ] E2E tests pass

**Status**: Draft | **Created**: 2026-03-28
