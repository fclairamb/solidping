---
model: opus
effort: medium
---

# Run plugin check types through an HTTP service that speaks the plugin protocol

## Problem

Spec `2026-10-17-04-checker-plugins.md` adds check-type plugins written in JS or shipped as
local executables. Some plugins fit better as a service: a heavy runtime (JVM, Python with
native libraries), a connection pool kept open to the target between runs, or a probe service
a team already operates. Today such a service can only be watched through a `js` or `http`
check that parses its answer by hand, with no type, schema or form.

Three constraints come from the analysis in spec 04:
- the probe runs from wherever the service is deployed, so regional results stay true only if
  the service sits next to the worker that calls it (a sidecar on loopback);
- requests carry decrypted secrets, so the plugin must authenticate the caller and traffic
  outside loopback must use TLS;
- the service's own network calls bypass the egress guard, SSH tunnels and `ipVersion`.

## Depends on

Spec `2026-10-17-04-checker-plugins.md`, all phases: dynamic registry, manifest loader, schema
validation, generic config with `SecretFielder`, result caps, dash0 JSON editor, capability
routing (step 17) and stored manifests (step 18). Implement it first.

## Decisions (2026-10-17)

1. The contract is the plugin protocol of spec 04, carried over HTTP with three endpoints:
   `GET /checkers`, `POST /validate`, `POST /execute`. Request and response bodies are the
   message types the exec transport uses (spec 04 step 15), so the protocol is documented once.
2. The reference plugin is a new check type, `x-finger` (Finger, RFC 1288), picked as a
   little-used, dated network protocol, like `x-gopher` in spec 04.

## Proposal

1. Shared protocol types in `server/internal/plugins/protocol` (created by spec 04 step 15):
   - `TypeDescriptor`: `type`, `description`, `labels`, `schema` (inline JSON Schema),
     `minPeriod`, `defaultPeriod`, `validate` (bool). Same fields as a manifest type entry,
     with the schema inlined.
   - `ValidateRequest{type, config}` → `ValidateResponse{errors: [{field, message}]}`, an empty
     list meaning valid.
   - `ExecuteRequest{type, config, deadline}` (deadline in RFC 3339) →
     `ExecuteResponse{status, metrics, output}`, status one of `up`, `down`, `warning`,
     `timeout`, `error`.
   - `ProtocolVersion = 1`.
2. Manifest for an HTTP plugin, in the plugin directory and read by the spec 04 loader:
   ```json
   {
     "name": "finger",
     "protocol": "http",
     "url": "http://127.0.0.1:7979",
     "secretEnv": "SP_PLUGIN_FINGER_KEYS"
   }
   ```
   No `types` and no `version`: they come from `GET /checkers`. `secretEnv` names the
   environment variable holding the signing key set, in the JSON form
   `servicesig.ParseKeySet` reads (`[{"id":"2026-10","secret":"…"}]`,
   `server/internal/servicesig/servicesig.go:114`). The loader refuses the plugin (logs and
   skips, never stops the boot) when: the URL is not `http` or `https`, the URL is `http://` to
   a non-loopback host, the variable is unset or empty, or the key set does not parse. A
   non-loopback `https://` URL loads with one warning: the probe runs from the service's host,
   not from the worker's region.
3. Describe: the node calls `GET /checkers` at load time, then every 60 s. Response:
   `{"protocolVersion": 1, "version": "1.2.0", "data": [TypeDescriptor, …]}`.
   - Each descriptor is checked like a manifest type in spec 04 step 5 (`x-` prefix, unique
     across all plugins, schema compiles). An invalid descriptor is skipped; an unknown
     `protocolVersion` refuses the whole plugin.
   - On success the node registers or updates the types, advertises them (spec 04 step 17)
     and sends the descriptors to the server like an agent's manifests (spec 04 step 18).
     The API can then validate and render the form even when it does not host the plugin, and
     while the service is down.
   - A type that disappears from the response is unregistered on that node. Its checks show
     "no capable worker" (spec 04 step 17) and are never deleted.
   - On failure the node keeps the last good descriptors. After 3 consecutive failed refreshes
     it stops advertising the types, so jobs go to nodes whose service is up. The next success
     resumes advertising.
4. Execute: the worker sends `POST /execute` with an `ExecuteRequest`. The config includes the
   decrypted secrets (merged at dispatch by `checkjobsvc.MergeJobSecrets`) and the deadline is
   the job's execution deadline, also set on the request context. The worker measures
   `Duration` itself. Mapping:
   - 200 with a valid body → that result, through the spec 04 step 10 caps;
   - deadline exceeded → `timeout`;
   - connection refused, non-200 status, body over 1 MiB (stop reading at the cap), invalid
     JSON, unknown status → `error`, with `Output.message` naming the plugin failure. Never
     `down`: a broken plugin is not a down target.
5. Validate: `POST /validate` is called only when the descriptor has `"validate": true` and the
   node handling the save hosts the plugin. It runs after the schema validation of spec 04
   step 7. A non-empty `errors` list returns `VALIDATION_ERROR` (400) with the field messages.
   Unreachable service, non-200 or no answer within 2 s: logged as a warning, and the save
   goes through on the schema alone.
6. HTTP client: one per plugin, keep-alive, TLS verification always on, no redirects (a
   redirect could send the secrets elsewhere: any 3xx maps to `error`), no proxy from the
   environment. It does not go through the egress guard: the URL is operator configuration,
   usually loopback, which the guard refuses in SaaS mode.
7. Authentication: every request is signed with `servicesig.SignRequest` using the current key
   of the manifest's key set (`X-SP-Signature: v1,<base64 HMAC-SHA256>`, `X-SP-Timestamp`,
   `X-SP-Key-Id`, canonical string `<timestamp>.<METHOD>.<path>.<hex sha256(body)>`,
   `servicesig.go:144`). The plugin verifies it and rejects a timestamp outside
   `servicesig.MaxSkew` (300 s). Keeping several keys in the set allows rotation.
8. HTTP plugin types are forced to the `unsafe` label, like exec and nagios types (spec 04
   step 19): disabled by default in SaaS mode on cloud workers, allowed on private-location
   agents.
9. Reference plugin `x-finger`:
   - Handler in `server/internal/plugins/examples/finger` (standard library only). It verifies
     the signature with `servicesig.VerifyRequest`, answers 401 otherwise, and implements the
     three endpoints. `server/cmd/plugin-finger` serves it (`-listen 127.0.0.1:7979`, key set
     from `SP_PLUGIN_FINGER_KEYS`). Both are compiled and linted with the server, so the
     example cannot rot. Its manifest sits next to the handler, outside the repo's `plugins/`
     folder, so `make dev` does not try to reach a service it does not start.
   - Config: `host` (string, required), `port` (integer 1–65535, default 79), `user` (string,
     default `""`, which lists logged-in users), `contains` (string, optional).
   - Run: connect to `host:port`, write `user + "\r\n"`, read until the server closes the
     connection (cap 64 KiB), within the deadline.
   - `down` when the connection fails, the response is empty or `contains` is set and absent.
     `timeout` when the deadline expires. `up` otherwise. Metrics: `bytes`, `lines`. Output
     `message`: the first response line, trimmed.
   - Example host in docs and fixtures: `finger.acme.com`, never a real server.
10. Docs: an "HTTP plugins" section in `web/docs/docs/features/plugins.md` (created by spec 04):
    the three endpoints with request and response examples, how to verify the signature in any
    language, why the service must sit next to the worker, the failure mapping, and the finger
    example with the commands to run it.

## Tests

- Loader: a valid http manifest triggers describe and registers the types; `http://` to a
  non-loopback host, an unset `secretEnv` variable and an unparsable key set are each refused
  with an error log; a non-loopback `https://` URL loads with exactly one warning.
- Describe (fake plugin on `httptest`): `protocolVersion` 2 refuses the plugin; a descriptor
  without the `x-` prefix, one colliding with another plugin's type, and one whose schema does
  not compile are skipped while the valid ones register; a refresh adds and removes types;
  3 failed refreshes stop advertising and the next success resumes it (positive control: the
  type is advertised before the failures).
- Execute: 200 with `up`, `down`, `warning`; a plugin slower than the deadline → `timeout`;
  500, invalid JSON and an unknown status → `error`; a body over 1 MiB → `error` without being
  read past the cap; a 302 → `error`, and the redirect target receives no request (positive
  control: the same target gets the request when it is the manifest URL).
- Signature: every request the worker sends passes `servicesig.VerifyRequest` against the
  manifest key set; after rotation (new key first in the set) requests carry the new key id.
- Secrets: a secret config field reaches the plugin in `/execute` and appears neither in the
  logs nor in the stored result.
- Validate: `"validate": true` with errors → `VALIDATION_ERROR` with the messages; service down
  → the save succeeds on the schema alone and logs a warning; `"validate": false` → the
  endpoint is never called (positive control: it is called once with `true`).
- Reference plugin, against a fake Finger server on `127.0.0.1`: a response → `up` with
  `lines` and `bytes`; `contains` absent → `down`; closed port → `down`; silent server with a
  short deadline → `timeout`; an unsigned request → 401.
- `AdvertisedCheckTypeCount()` is unchanged with the finger plugin loaded.

## To verify

- How a cloud worker sends descriptors to the server: spec 04 step 18 covers agents; check
  that cloud workers can use the same path.
- How the save path knows whether its own node hosts a plugin, in a single-binary deployment
  and when `SP_NODE_ROLE` splits API and workers.
