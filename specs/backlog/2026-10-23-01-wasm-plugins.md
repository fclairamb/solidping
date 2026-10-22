---
model: opus
effort: high
---

# WebAssembly plugin transport

**Status:** backlog. Not scheduled: pick it up when a user asks for a sandboxed plugin written
in a compiled language. Builds on `specs/done/2026/10/2026-10-17-05-checker-plugins.md` and
`specs/done/2026/10/2026-10-17-06-http-plugins.md`.

## Problem

Plugins (`server/internal/plugins/`) have four transports:

| Transport | Sandboxed | Languages | Ships as |
|---|---|---|---|
| `js` | yes (checkjs runtime, egress guard, tunnels) | JavaScript only | one script |
| `exec` | no | any | one binary per OS/arch |
| `nagios` | no | any | one binary per OS/arch |
| `http` | no (runs elsewhere) | any | a service the operator runs |

There is no way to write a sandboxed plugin in Go or Rust. An operator who wants a compiled
plugin has to use `exec`, which is labelled `unsafe`, runs with the server's privileges and
needs a build for every agent architecture.

A `wasm` transport closes that gap: one `.wasm` file runs on every server and agent, cannot
touch the filesystem or the server's environment, and reaches the network only through
functions the server provides, which apply the same egress guard as a js check.

## Proposal

### 1. Manifest

New protocol `"wasm"` in `manifest.go` (`ProtocolWASM`). `entry` names the module, relative to
the plugin folder, like `js`. Everything else (`types`, schemas, periods) is unchanged.

```json
{
  "name": "finger-wasm",
  "version": "1.0.0",
  "protocol": "wasm",
  "entry": "finger.wasm",
  "types": [{ "type": "x-finger-wasm", "description": "...", "schema": "schema.json", "labels": ["category:network"] }]
}
```

- `validate: true` is refused for `wasm` at load (`ErrInvalidManifest`): the schema is the
  only validator in this version.
- `pluginLabels` treats `wasm` like `js`: `safe` unless the manifest says `unsafe`
  (`unsafe := protocol != ProtocolJS && protocol != ProtocolWASM`). Update its doc comment.
- Entry cap: 16 MiB (`ErrTooLarge` above it).

### 2. Runtime

[wazero](https://github.com/tetratelabs/wazero), pure Go, no cgo. No Extism: its HTTP host
function uses its own client and would skip the egress guard.

- New file `server/internal/plugins/wasm.go`, `wasmTransport`, wired in `newTransport`
  (`loader.go`).
- At load: one `wazero.Runtime` per plugin, compile the module once (`CompiledModule`). A
  module that does not compile, or imports anything other than `wasi_snapshot_preview1` and the
  `solidping` host module below, fails the plugin at load, never the boot.
- Per run: a fresh module instance, closed at the end. No state is shared between runs, so two
  orgs' checks never see each other's memory.
- Limits:
  - memory: 64 MiB (`WithMemoryLimitPages(1024)`);
  - time: the check timeout (`cfg.timeout()`), enforced with `WithCloseOnContextDone(true)`, so
    a busy loop is stopped at the deadline and the run ends in `StatusTimeout`;
  - at most 16 open connections per run, 4 MiB read and 4 MiB written per run in total;
  - stderr is captured up to 16 KiB and logged at debug level with the plugin name, never put
    in the result.
- WASI: no preopened directory, no environment variables, no arguments. Clock and random are
  allowed (latency measurement needs the clock).
- `close()` closes the runtime.

### 3. Exchange

The module is a WASI command (`_start`), so `GOOS=wasip1 GOARCH=wasm go build` and Rust's
`wasm32-wasip1` target both produce one without extra tooling.

- stdin: `protocol.ExecuteParams` as JSON (type, config with secrets, deadline).
- stdout: `protocol.ExecuteResult` as JSON, 1 MiB max. Parsed and mapped exactly as the exec
  transport does, then `LimitResult`.
- A non-zero exit, a trap or unparsable stdout gives `StatusError` with the reason in
  `Output["error"]`.

### 4. Host functions (module `solidping`)

The only way out of the sandbox. Strings and buffers are `(ptr, len)` pairs into the module's
memory. Every function returns an `errno` (`0` = ok); `last_error` gives the message of the last
failure.

| Function | Purpose |
|---|---|
| `tcp_connect(addr_ptr, addr_len, tls, handle_out) -> errno` | Dial `host:port`. `tls = 1` wraps the connection in TLS with SNI from the host. |
| `conn_read(handle, buf_ptr, buf_len, n_out) -> errno` | Read up to `buf_len` bytes, bounded by the run deadline. EOF is its own errno. |
| `conn_write(handle, buf_ptr, buf_len, n_out) -> errno` | Write, bounded by the run deadline. |
| `conn_close(handle) -> errno` | Close. Every handle is closed at the end of the run anyway. |
| `http_request(req_ptr, req_len, resp_ptr, resp_cap, resp_len_out) -> errno` | JSON request `{method, url, headers, body}` (body base64), JSON response `{status, headers, body, durationMs}`. A response bigger than `resp_cap` returns a "buffer too small" errno with the needed size in `resp_len_out`, so the module can retry with a bigger buffer. |
| `last_error(buf_ptr, buf_len, len_out) -> errno` | Message of the last failed call. |

Each network call goes through the same guards as checkjs, reusing its code rather than
copying it:

- `tcp_connect`: `egress.HostAllowlistFrom(ctx).Check(address)`, then the tunnel dialer from
  the context when the check runs through a tunnel, else
  `checkerdef.GuardDialerOr(ctx, &net.Dialer{})`, as `checkjs/socket.go` does. DNS is resolved
  host side, so the guard sees the real IP.
- `http_request`: the same transport as checkjs
  (`egress.HostAllowlistFrom(ctx).RoundTripper(checkerdef.HTTPTransportFor(ctx, false))`).

If extracting the dial logic from `checkjs/socket.go` into a shared helper is needed so
`plugins` does not import `checkjs`, do it in this spec (the `RunJS` hook in `js.go` exists
to keep checkjs out of the `sp` CLI; keep that property).

### 5. Example and SDK

- `server/internal/plugins/examples/finger-wasm/`: the finger example ported to Go,
  `GOOS=wasip1 GOARCH=wasm`, using `//go:wasmimport solidping ...`. A small `sdk.go` in the same
  folder wraps the host functions (`Dial`, `Read`, `Write`, `Close`, `HTTP`) so the example
  stays readable. No published SDK module in this version.
- The `.wasm` is not committed: the test builds it with the Go toolchain into a temp dir.

### 6. Docs

`web/docs/docs/features/plugins.md`: a `wasm` section with the manifest, the exchange, the host
function table, the limits, and how to build with Go and Rust. Changelog entry per
`wiki/conventions/changelog.md`.

## Non-goals

- WASI preview 2 / component model (wazero does not support it).
- UDP, raw sockets, ICMP, DNS queries as host functions. Add them when a plugin needs them.
- A `validate` call into the module.
- Sub-checks (`solidping.check(...)`) from a wasm plugin.
- An on-disk compilation cache. Compile at boot; revisit if boot time suffers.
- Publishing an SDK as a separate Go module or Rust crate.

## Open questions

1. **Memory cap configurable?** Recommended: no, fixed 64 MiB. Add a server setting when
   someone hits it.
2. **Compiler or interpreter?** Recommended: wazero's default (compiler on amd64/arm64,
   interpreter elsewhere). No setting.
3. **TLS options on `tcp_connect`** (skip verify, client certs)? Recommended: none in this
   version; a plugin that needs them uses `http_request` or `exec`.

## Tests

- Load: a valid wasm plugin registers its types with the `safe` label; a module importing an
  unknown host module, a module over 16 MiB, a broken module and `validate: true` each fail that
  plugin only, and the boot goes on.
- Run (example built at test time): finger against a local TCP server gives `StatusUp` with
  the expected output and metrics.
- Sandbox:
  - a module that loops forever ends in `StatusTimeout` at the check timeout;
  - a module that allocates past 64 MiB ends in `StatusError`;
  - a module that tries to open a file gets an error (no preopens);
  - the server environment (e.g. a set `SP_` variable) is not visible to the module.
- Egress: `tcp_connect` and `http_request` to a host refused by the allowlist / egress guard
  fail with the same message as a js check; a tunneled check dials through the tunnel.
- Caps: a 17th connection, and reading past 4 MiB, are refused.
- Isolation: two consecutive runs of a module that keeps a global counter both see it at its
  initial value.
- `LimitResult` applies to wasm results (oversized output is truncated and listed).

## Acceptance criteria

- [ ] `protocol: "wasm"` loads, runs and reports types from agents like `js`.
- [ ] Wasm types are labelled `safe` by default.
- [ ] Every network call from a module goes through the egress guard and tunnels.
- [ ] Time, memory, connection and byte limits are enforced and tested.
- [ ] The finger-wasm example builds with the stock Go toolchain and passes its test.
- [ ] `plugins.md` documents the transport; changelog entry added.
- [ ] `make lint` and `make test` pass.
