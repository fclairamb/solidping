---
sidebar_position: 22
title: Embedded devices (TCP/UDP)
---

# Embedded devices: push heartbeats over TCP or UDP

Heartbeat checks are SolidPing's passive "this thing is alive" primitive, but
until now the only way to reach one was HTTPS. That shuts out exactly the
senders heartbeat semantics fit best:

- TLS costs tens of kilobytes of RAM, a CA store, and a correct clock for
  certificate validation — often unavailable on an ESP8266 or an STM32-class
  microcontroller.
- HTTP framing is pure overhead for a message whose whole meaning is one bit.
- A battery-powered sensor wants **one fire-and-forget datagram**, not a
  connection handshake.

The embedded push transports are two tiny listeners that accept one
newline-delimited line per beat and feed the **same** ingest as the HTTPS
endpoint — same result rows, same incidents, same recovery. There is no new
check type: an existing heartbeat check simply gains two more doors.

:::info Off by default
Both listeners are disabled unless an operator enables them, and exposing their
ports is a deployment decision (a Kubernetes `Service`, a load-balancer rule, a
firewall). See [Enabling the listeners](#enabling-the-listeners).
:::

## The wire protocol

One line per beat, still `netcat`-debuggable. Fields are separated by exactly
one space; a run of spaces is rejected.

### SP1 — plaintext token

```
SP1 <org>/<check-identifier> <token> [annotation]\n
```

```bash
# TCP
printf 'SP1 acme/sensor-1 <TOKEN>\n' | nc beats.example.com 4001

# UDP — fire and forget
printf 'SP1 acme/sensor-1 <TOKEN>' | nc -u -w1 beats.example.com 4001
```

`<check-identifier>` is the check's slug or UID, exactly as in the HTTPS ping
URL. The token is the one shown on the check's detail page.

### SP2 — HMAC-signed, no secret on the wire

```
SP2 <org>/<check-identifier> <ts> <ctr> [annotation] <mac>\n
```

- **`ts`** — unix seconds from the device clock, or `0` when the device has no
  clock.
- **`ctr`** — a strictly increasing counter (see
  [the counter recipe](#the-counter-recipe)).
- **`mac`** — HMAC-SHA256 keyed with the check's token, computed over the exact
  line **up to but excluding the final space and the MAC itself**, truncated to
  16 bytes and hex-encoded.

The MAC is always the **last** space-separated token. That is what puts the
annotation inside the signature: everything between the counter and the MAC is
covered, so an annotation on a signed beat cannot be tampered with in flight.

A bare SP2 datagram is around 80 bytes.

```
signed bytes:  SP2 acme/sensor-1 1754640000 4294967297 started volts=3.71
sent line:     SP2 acme/sensor-1 1754640000 4294967297 started volts=3.71 <mac>
```

Verification happens in this order:

1. Resolve the check, recompute the MAC with its token, compare in constant
   time.
2. If `ts != 0`, require it within ±5 minutes of server time (configurable).
3. Require `ctr` **strictly greater** than the last accepted counter for that
   check, then persist the new value.

Step 3 is the replay protection: an old datagram fails, and even the most
recent datagram cannot be sent twice. Step 2 exists only to bound the damage if
the server's counter state is ever lost — a restored backup, say.

### The counter recipe

`ctr` is a counter, not a nonce, precisely so a device needs no RNG and almost
no flash wear. Persist a boot counter (one flash write per boot) and send:

```c
ctr = ((uint64_t)boot_count << 32) | seconds_since_boot;
```

That is monotonic across reboots, costs zero flash writes in steady state, and
survives a power cut. Counters above 2^63−1 are rejected.

### A minimal Arduino / ESP sketch

```cpp
// Requires an HMAC-SHA256 implementation — mbedTLS ships with the ESP32 core,
// and a self-contained one is about 200 lines. Nothing like a TLS stack.
#include <WiFiUdp.h>
#include <mbedtls/md.h>

const char *ORG   = "acme";
const char *CHECK = "sensor-1";
const char *TOKEN = "…";               // from the check detail page
const char *HOST  = "beats.example.com";
const uint16_t PORT = 4001;

uint64_t bootCount;                     // restored from flash at boot

void sendBeat(float volts) {
  char line[192];
  uint64_t ctr = (bootCount << 32) | (millis() / 1000);

  // ts = 0: this device has no clock. Perfectly fine — the counter is what
  // stops replays.
  int n = snprintf(line, sizeof(line), "SP2 %s/%s 0 %llu volts=%.2f",
                   ORG, CHECK, (unsigned long long)ctr, volts);

  uint8_t mac[32];
  mbedtls_md_context_t ctx;
  mbedtls_md_init(&ctx);
  mbedtls_md_setup(&ctx, mbedtls_md_info_from_type(MBEDTLS_MD_SHA256), 1);
  mbedtls_md_hmac_starts(&ctx, (const uint8_t *)TOKEN, strlen(TOKEN));
  mbedtls_md_hmac_update(&ctx, (const uint8_t *)line, n);
  mbedtls_md_hmac_finish(&ctx, mac);
  mbedtls_md_free(&ctx);

  n += snprintf(line + n, sizeof(line) - n, " ");
  for (int i = 0; i < 16; i++) {          // truncate to 16 bytes, hex-encode
    n += snprintf(line + n, sizeof(line) - n, "%02x", mac[i]);
  }

  WiFiUDP udp;
  udp.beginPacket(HOST, PORT);
  udp.write((const uint8_t *)line, n);
  udp.endPacket();
}
```

## Annotations: send data with the beat

Both message forms accept an optional annotation after the target — a status
word, then `key=value` fields:

```
started volts=3.71 rssi=-67 fw=1.4.2
```

- The **status word** is one bare token with no `=`. It is stored as an opaque
  annotation today; `started`, `ok` and `fail` are reserved so firmware can
  adopt the vocabulary now, but none of them changes the check's status yet.
  The immediate value is reboot visibility: a device beating `started` every
  ten minutes is crash-looping, which a bare heartbeat cannot distinguish from
  healthy.
- **Numeric values become metrics** — first-class time series on the check
  page, rolled up by the usual
  [aggregation suffix conventions](./observability.md) (a bare name averages;
  `_max`, `_min`, `_sum`, `_cnt` and friends pick another rollup). Battery
  volts, RSSI and temperature in one UDP datagram.
- **Non-numeric values** are stored on the result's output alongside the status
  word.

Limits per beat: at most 10 pairs, keys `[a-z0-9_]` up to 32 characters, values
up to 64 bytes with no spaces (there is no quoting), and 128 bytes for the whole
annotation.

:::tip An annotation never fails a beat
Parsing is best-effort. If the annotation does not match the grammar it is
stored as raw text and **the beat still counts**. A typo in a key name must
never make a healthy device look dead.
:::

The HTTPS ingest accepts the same grammar, via `?annotation=…` or an
`"annotation"` key in the JSON body, so the field means one thing on all three
transports.

## What each transport does

### UDP

One datagram carries exactly one line. On success the server optionally replies
`OK` — never more bytes than it received, so the listener can never be used as
an amplification vector.

**On any failure it replies nothing at all.** A malformed line, an unknown
organization, an unknown check, a wrong token, a bad MAC, a replayed counter
and a rate-limited source are all byte-identical silence. That is deliberate:
any distinction would be a free oracle telling an unauthenticated attacker
which checks exist and when a guessed token is getting close.

### TCP

The stream is newline-delimited and each line is an independent beat, answered
`OK\n`. A device may send one beat and close, or hold the connection open and
keep beating on it — one handshake in total, and the NAT pinhole stays warm,
which matters a great deal on cellular.

An invalid line closes the connection with no response.

:::warning An open connection is not a heartbeat
Only an accepted **line** marks the check alive. A hung device holding a socket
open never reads as up. After any disconnect, reconnect with jittered backoff.
:::

Guards: an idle timeout (10 minutes by default), a per-source beat budget, a
bounded line length (512 bytes) and a connection cap.

## Requiring signed beats

Each heartbeat check has a `require_hmac` option, off by default:

| `require_hmac` | SP1 (plaintext) | SP2 (signed) | HTTPS |
|---|---|---|---|
| `false` (default) | accepted | accepted | accepted |
| `true` | **rejected** | accepted | accepted |

Turn it on from the check's detail page, or over the API:

```bash
curl -X PATCH -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"config":{"require_hmac":true}}' \
  'https://solidping.example.com/api/v1/orgs/acme/checks/sensor-1'
```

:::danger Rotate the token when you turn this on
A check that has ever accepted SP1 has put its token in cleartext on the wire,
where any passive listener could have taken it — and that same token is the SP2
signing key. Turning on `require_hmac` without rotating leaves an attacker who
already captured the token able to forge perfectly valid signed beats. Use
**Regenerate** on the check detail page immediately after.
:::

## Security, stated plainly

- **SP1 sends the token in cleartext, and a captured beat can be replayed to
  mask a real outage.** That is exactly what `require_hmac` exists to close.
  Treat SP1 as a getting-started mode on a trusted network.
- **SP2 authenticates the message, not the transport.** An attacker can still
  drop datagrams — UDP guarantees nothing anyway. The HMAC protects against
  forged aliveness, not against censorship. A device that is being jammed looks
  down, which is the correct outcome.
- **UDP source addresses are spoofable.** SolidPing records the source address
  for forensics and uses it as a rate-limiting bucket key; it is never treated
  as identity.
- **Nothing on these ports is an oracle.** Failures are silent on UDP and close
  the connection on TCP, and neither ever names a reason.

## Enabling the listeners

| Setting | Environment variable | Default |
|---|---|---|
| `heartbeat.tcp_listen` | `SP_HEARTBEAT_TCP_LISTEN` | *(off)* |
| `heartbeat.udp_listen` | `SP_HEARTBEAT_UDP_LISTEN` | *(off)* |
| `heartbeat.timestamp_window` | `SP_HEARTBEAT_TIMESTAMP_WINDOW` | `5m` |
| `heartbeat.idle_timeout` | `SP_HEARTBEAT_IDLE_TIMEOUT` | `10m` |
| `heartbeat.rate_per_minute` | `SP_HEARTBEAT_RATE_PER_MINUTE` | `120` |
| `heartbeat.rate_burst` | `SP_HEARTBEAT_RATE_BURST` | `60` |
| `heartbeat.max_source_ips` | `SP_HEARTBEAT_MAX_SOURCE_IPS` | `10000` |
| `heartbeat.max_connections` | `SP_HEARTBEAT_MAX_CONNECTIONS` | `512` |
| `heartbeat.udp_reply_ok` | `SP_HEARTBEAT_UDP_REPLY_OK` | `true` |

```bash
SP_HEARTBEAT_TCP_LISTEN=:4001
SP_HEARTBEAT_UDP_LISTEN=:4001
```

**Port 4001, the same number on TCP and UDP** — one number to document, one
firewall rule, adjacent to the app's own 4000. The protocol version travels in
the `SP1`/`SP2` line prefix, never in the port, so one port serves every future
message form. A bare port (`4001`) or a plain `true` both normalize to `:4001`.

Once a listener is up, the check detail page shows ready-made `nc` one-liners
for that check next to its HTTPS URL.

### Kubernetes

Enabling the setting binds the port inside the pod; reaching it from outside is
still up to your `Service` and load balancer. SolidPing does not automate that.

```yaml
ports:
  - name: beats-tcp
    port: 4001
    targetPort: 4001
    protocol: TCP
  - name: beats-udp
    port: 4001
    targetPort: 4001
    protocol: UDP
```

Note that many cloud load balancers handle TCP and UDP on separate resources,
and some do not forward UDP at all.

### Metrics

Both listeners export `solidping_heartbeat_push_beats_total`, labelled by
`transport` (`tcp`/`udp`) and `outcome` (`accepted`, `rejected`, `malformed`,
`rate_limited`, `error`), plus
`solidping_heartbeat_push_connections_total` for TCP accepts and refusals. Only
`error` means a server fault — everything else is normal traffic on a public
port.
