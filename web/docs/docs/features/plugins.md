---
sidebar_position: 28
title: Check plugins
---

# Check plugins

A check plugin adds a check type without changing SolidPing itself: a niche
protocol, an in-house probe, an existing Nagios `check_*` script. The plugin
ships its own config schema and its own code. SolidPing validates configs
against the schema, stores them like any other check, encrypts the fields the
schema marks secret, and runs the plugin on the workers that loaded it.

Plugins are loaded from a directory set by the operator:

```bash
SP_PLUGINS_DIR=/etc/solidping/plugins solidping serve
```

`SP_PLUGINS_DIR` (config key `plugins.dir`) is unset by default, which turns
plugins off. A private-location agent reads its own `SP_PLUGINS_DIR`, so an
agent can run plugins the cloud workers do not have.

When an agent connects, it sends the manifest and schema of each plugin type it
loaded. The server keeps them for the agent's organization only, so the check
form and validation of those types keep working while the agent is offline.
Another organization never sees them. A platform (system) agent's plugin types
are not kept: install those plugins on the server too. A report holds at most
32 types and 64 KiB of schema per type.

## The plugin folder

One folder per plugin, each with a `manifest.json`:

```text
plugins/
  gopher/
    manifest.json
    schema.json
    check.js
```

```json
{
  "name": "gopher",
  "version": "1.0.0",
  "protocol": "js",
  "entry": "check.js",
  "types": [{
    "type": "x-gopher",
    "description": "Check a Gopher server (RFC 1436)",
    "labels": ["safe", "category:network"],
    "schema": "schema.json",
    "minPeriod": "1m",
    "defaultPeriod": "5m"
  }]
}
```

| Field | Meaning |
|---|---|
| `name`, `version` | Shown with the type in the API (`plugin: {name, version}`) |
| `protocol` | `js`, `exec` or `nagios`, see below |
| `entry` | The script or executable, relative to the plugin folder. It cannot point outside the folder, and a check config can never name a command |
| `args` | `nagios` only: the argument template, see below |
| `types[].type` | The check type. It must start with `x-` and use `a-z`, `0-9` and `-`, so it never collides with a built-in type |
| `types[].schema` | JSON Schema (draft 2020-12) of the check's `config`, relative to the plugin folder |
| `types[].labels` | `safe` or `unsafe`, plus a `category:*` label for the type picker |
| `types[].minPeriod`, `defaultPeriod`, `maxPeriod` | Go durations (`30s`, `5m`) |
| `types[].validate` | `exec` only: also ask the plugin to validate a config the schema accepted |

A plugin that fails to load is logged and skipped. It never stops the server
or the other plugins. The causes: a missing or invalid manifest, a type
without the `x-` prefix, a type declared twice, a schema that does not
compile, a missing entry file, or an entry outside the plugin folder.

Plugin types are not counted in the number of check types SolidPing advertises.

## The schema is the validator

For a built-in type the published JSON Schema only describes the config. For a
plugin type the manifest schema **is** the validator: a config it rejects is
refused on create and update with `VALIDATION_ERROR`, naming the key. The
plugin itself is not contacted.

Keys listed in `x-solidping-secret-fields` are stored encrypted and left out of
exports, exactly like a built-in password field:

```json
{
  "type": "object",
  "properties": {
    "host": {"type": "string"},
    "token": {"type": "string"}
  },
  "required": ["host"],
  "x-solidping-secret-fields": ["token"]
}
```

The schema is served at `GET /api/v1/checks/schema/x-gopher`, with an
`x-solidping-plugin` key naming the plugin.
`GET /api/v1/orgs/{org}/check-types/x-gopher/schema` serves the same document,
plus the types the organization's agents reported. The dashboard has no form for a
plugin type: it edits the config as JSON and checks it against this schema as
you type.

## JavaScript plugins (`protocol: js`)

The entry script runs in the same sandboxed runtime as a
[JavaScript check](/features/javascript-checks): `http`, `tcp`, `udp`,
`websocket`, `solidping.check()`, the egress policy, SSH tunnels and the
deadline all apply, and it runs on cloud workers and agents with nothing to
install. The script is capped at 64 KiB and must compile at load time.

The validated config is the read-only `config` global, secret fields included.
The script returns the same object as a `js` check:

```js
var conn = tcp.connect(config.host + ":" + (config.port || 70));
if (!conn.ok) {
  return { status: "down", output: { message: "connect failed", error: conn.error } };
}
// ...
return { status: "up", output: { message: firstLine }, metrics: { bytes: n } };
```

`status` is `up`, `down`, `timeout`, `error` or `warning`.

## Executable plugins (`protocol: exec`)

One long-lived process per plugin, started on first use, talking JSON-RPC 2.0
over stdin and stdout, one message per line. stderr goes to the server log. A
crashed process is restarted on the next call, with a backoff; requests in
flight when it died end with `error`.

```json
{"jsonrpc":"2.0","id":1,"method":"execute","params":{"type":"x-acme","config":{"host":"acme.com"},"deadline":"2026-10-17T12:00:05Z"}}
{"jsonrpc":"2.0","id":1,"result":{"status":"up","output":{"message":"ok"},"metrics":{"latency_ms":12}}}
```

With `"validate": true` on a type, the server also sends `validate`
(`{type, config}`) after the schema accepted a config, and expects
`{"errors": [{"field": "host", "message": "is not allowed"}]}` (empty when
valid).

The process gets a minimal environment (`PATH`, `HOME`, `LANG`, `TZ`,
`SOLIDPING_PLUGIN_DIR`), never the server's own.

## Nagios plugins (`protocol: nagios`)

The plugin is run once per check, with arguments built from the config through
the manifest's `args` template. `{{key}}` is replaced with the config value of
that key, each argument stays one argument (no shell), and an argument that
ends up empty is dropped:

```json
{
  "name": "nagios-http",
  "version": "1.0.0",
  "protocol": "nagios",
  "entry": "check_http",
  "args": ["-H", "{{host}}", "-p", "{{port}}"],
  "types": [{"type": "x-nagios-http", "description": "check_http", "schema": "schema.json"}]
}
```

| Exit code | Status |
|---|---|
| 0 | `up` |
| 1 | `warning` |
| 2 | `down` |
| 3 or anything else | `error` |

The first line of stdout becomes `output.message`. Perfdata after `|`
(`label=value[UOM];warn;crit;min;max`) becomes metrics; a malformed item is
ignored. When the check times out, the whole process group is killed.

## HTTP plugins (`protocol: http`)

The plugin is a service the operator runs, reached over HTTP. It fits a heavy
runtime (JVM, Python with native libraries), a connection pool kept open
between runs, or a probe service a team already operates. The manifest holds
no types and no version: they come from the service.

```json
{
  "name": "finger",
  "protocol": "http",
  "url": "http://127.0.0.1:7979",
  "secretEnv": "SP_PLUGIN_FINGER_KEYS"
}
```

`secretEnv` names the environment variable holding the signing key set, newest
key first:

```bash
SP_PLUGIN_FINGER_KEYS='[{"id":"2026-10","secret":"<random>"}]'
```

The plugin is skipped (logged, the boot goes on) when the URL is not `http` or
`https`, when it is `http://` to a host other than loopback (requests carry
decrypted secrets), or when the variable is unset, empty or not a valid key
set. An `https://` URL to another host loads with a warning.

### Endpoints

The bodies are the messages of the exec protocol, without the JSON-RPC
envelope.

`GET /checkers` lists the types. The node calls it at load, then every 60 s:

```json
{
  "protocolVersion": 1,
  "version": "1.2.0",
  "data": [{
    "type": "x-finger",
    "description": "Check a Finger server (RFC 1288)",
    "labels": ["category:network"],
    "schema": {"type": "object", "properties": {"host": {"type": "string"}}, "required": ["host"]},
    "minPeriod": "1m",
    "defaultPeriod": "5m",
    "validate": true
  }]
}
```

- Each type is checked like a manifest type (`x-` prefix, unique across
  plugins, schema that compiles). An invalid one is skipped, the others load.
  An unknown `protocolVersion` refuses the whole plugin.
- A type that disappears from the list is unregistered on that node. Its
  checks are kept and wait for a worker that has it.
- When the call fails, the node keeps the last good list. After 3 failures in
  a row it stops advertising the types, so jobs go to nodes whose service is
  up. The next success advertises them again.
- An agent sends the list to the server like any plugin manifest, again when
  it changes.

`POST /execute` runs one check:

```json
{"type": "x-finger", "config": {"host": "finger.acme.com", "port": 79}, "deadline": "2026-10-17T12:00:05Z"}
```

```json
{"status": "up", "metrics": {"bytes": 312, "lines": 6}, "output": {"message": "Login: alice"}}
```

`POST /validate` is called only for a type with `"validate": true`, after the
schema accepted the config, on a node that hosts the plugin:

```json
{"type": "x-finger", "config": {"host": "finger.acme.com", "user": "alice"}}
```

```json
{"errors": [{"field": "user", "message": "must be a single line"}]}
```

An empty list means valid. Errors are answered as `VALIDATION_ERROR`. A
service that is down, answers non-200 or takes over 2 s is logged, and the save
goes through on the schema alone.

### Failure mapping

| What happened | Status |
|---|---|
| 200 with a valid body | the plugin's status, through the result caps |
| No answer before the check deadline | `timeout` |
| Connection refused, non-200, a redirect, a body over 1 MiB, invalid JSON, unknown status | `error`, `output.message` names the failure |

A broken plugin is never `down`: that status is for the target. Redirects are
not followed, since they could send the secrets elsewhere. The client verifies
TLS, ignores proxy variables, and does not go through the egress guard: the URL
is operator configuration.

### Verifying the signature

Every request carries three headers:

| Header | Value |
|---|---|
| `X-SP-Timestamp` | Unix seconds |
| `X-SP-Key-Id` | id of the key that signed |
| `X-SP-Signature` | `v1,` then base64 HMAC-SHA256 |

The signed string is
`<timestamp>.<METHOD>.<path>.<hex sha256 of the body>`, where `path` is the
request path (with the URL's own prefix, if any) and the body is empty for
`GET /checkers`. To verify, in any language: find the key by its id, rebuild
the string, compute the HMAC with that key's secret, compare in constant time,
and reject a timestamp more than 300 s away from your clock. Answer 401
otherwise. Keeping two keys in the set (new first) lets you rotate without
downtime: SolidPing signs with the first one.

### Where the service runs

The check runs from wherever the service is deployed. Run it next to the
worker that calls it (a sidecar on loopback), or regional results stop being
true. Its own network calls also bypass what SolidPing applies to built-in
checks: the egress guard, SSH tunnels and `ipVersion`.

Only agents report HTTP plugin types to the server. A cloud worker does not.
When `SP_NODE_ROLE` splits the API node from the workers, load the same plugin
folder on the API node too, or it does not know the types and refuses to save
their checks. The API node must also reach the service, since it calls
`/validate` when a check is saved.

## Safety and limits

- `exec`, `nagios` and `http` types are always `unsafe`, whatever the manifest
  says.
  On a SaaS deployment they are off on the cloud nodes unless the operator
  lists them in `SP_CHECKERS_ENABLED`; private-location agents run them.
- A plugin-type job only goes to a worker that loaded the plugin: workers add
  their plugin types to their capability report. Saving a plugin check in a
  region where no live worker has the plugin returns a warning, and the check
  page shows the same reason (`regionWarnings`, code `REGION_NO_PLUGIN`) until
  a worker with the plugin is live there.
- The check-type enums of the OpenAPI spec list the built-in types only. A
  client must accept an unlisted `x-` type: the generated Go client and the
  `sp` CLI do.
- Every result is capped, whatever the protocol: an unknown status becomes
  `error`, `output` is cut to 16 KiB and at most 32 numeric metrics are kept.
  What was cut is listed in `output.truncated`.
- A run gets the check's `timeout` (at most 30 s).

## Worked example: the Gopher plugin

The repository ships `plugins/gopher`, a reference plugin for Gopher
(RFC 1436). It is not loaded unless `SP_PLUGINS_DIR` points at it (`make dev`
does).

| Config key | Default | Meaning |
|---|---|---|
| `host` | required | Server, e.g. `gopher.acme.com` |
| `port` | `70` | TCP port |
| `selector` | `""` (root menu) | Selector to request |
| `contains` | - | Text the response must contain |

It connects, sends `selector + "\r\n"` and reads until the `.` terminator line
or until the server closes the connection. The check is `down` when the
connection fails, the response is empty, the first line is a Gopher error item
(type `3`), or `contains` is missing; `timeout` when the deadline expires; `up`
otherwise. Metrics: `bytes` (response size) and `items` (menu lines). The
message is the first response line.

## Worked example: the Finger plugin

`x-finger` is the reference HTTP plugin, for Finger (RFC 1288). Its handler is
in `server/internal/plugins/examples/finger` and `server/cmd/plugin-finger`
serves it. Its manifest sits next to the handler, not in the repository's
`plugins/` folder, so `make dev` never tries to reach a service it does not
start.

| Config key | Default | Meaning |
|---|---|---|
| `host` | required | Server, e.g. `finger.acme.com` |
| `port` | `79` | TCP port |
| `user` | `""` (logged-in users) | User to query |
| `contains` | - | Text the response must contain |

It connects, sends `user + "\r\n"` and reads until the server closes the
connection (at most 64 KiB). The check is `down` when the connection fails,
the response is empty or `contains` is missing; `timeout` when the deadline
expires; `up` otherwise. Metrics: `bytes` and `lines`. The message is the first
response line. Its `validate` refuses a `user` spanning several lines.

To run it next to a worker:

```bash
export SP_PLUGIN_FINGER_KEYS='[{"id":"2026-10","secret":"'"$(openssl rand -hex 32)"'"}]'
cd server && go run ./cmd/plugin-finger -listen 127.0.0.1:7979 &
mkdir -p /etc/solidping/plugins/finger
cp internal/plugins/examples/finger/manifest.json /etc/solidping/plugins/finger/
SP_PLUGINS_DIR=/etc/solidping/plugins solidping serve
```

The service and SolidPing read the same `SP_PLUGIN_FINGER_KEYS`.
