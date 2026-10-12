---
model: sonnet   # haiku | sonnet | opus
effort: medium  # low | medium | high
---

# An operator cannot reproduce an HTTP check's request from a terminal

## Problem
When an HTTP check goes red, the first thing an operator does is replay the request by hand. Today they rebuild it field by field from the edit form: method, URL, headers, body, auth, redirects, HTTP version. The only cURL snippet in dash0 is the heartbeat one (`web/dash0/src/routes/orgs/$org/checks.$checkUid.index.tsx:340`), which is an inbound ping, not the probe.

What the probe sends is fixed in `server/internal/checkers/checkhttp/checker.go`:
- method defaults to GET, otherwise upper-cased (`:279-284`);
- the body is sent whenever it is non-empty, for every method (`:294-302`);
- basic auth first, then the default User-Agent, then `headers`, then `secretHeaders` last so they win (`:313-329`);
- up to `maxRedirects = 10` hops when following redirects (`:30`).

The config fields live in `server/internal/checkers/checkhttp/config/config.go`: `headers` (`:88`), `body` (`:89`), `basicAuth` (`:106`), legacy `username`/`password` (`:113`), `secretHeaders` (`:118`), `verifySsl` (`:138`), `followRedirects` (`:145`), `redirectHostPolicy` (`:153`), `httpVersion` (`:159`). The per-check `timeout` is a Go duration string in the generic config (`server/internal/db/models/check.go:450-465`).

How secrets come back from the API decides the design:
- `SecretFields()` is `basicAuth`, `password`, `secretHeaders` (`config.go:658-660`).
- The check read path strips every secret key from `config` and lists it in `configPrivateKeys` (`server/internal/handlers/checks/service.go:4174-4213`, field at `:878`, typed at `web/dash0/src/api/hooks.ts:196`).
- `secretHeaders` is removed as a whole map, so the client does not even know the header names. A `basicAuth` value is not returned either; a legacy `username` is public and does come back.
- The incident payload embeds `check.config` raw from the public column (`server/internal/handlers/incidents/service.go:2394-2400`). It has no `configPrivateKeys` and skips the read-time redaction the check endpoint applies.

So the client already has everything that is safe to show, and nothing that is not. Building the command client-side cannot leak a secret the API does not send. No API change is needed. That is why this spec is not security-sensitive.

## Proposal
1. Add a pure builder `web/dash0/src/lib/curl-command.ts`: `buildCurlCommand(config, configPrivateKeys): string`. Rules:
   - Every value is single-quoted. An embedded `'` becomes `'\''`. One flag per line, joined with ` \` and a newline.
   - `curl -i` first. HEAD becomes `--head` (curl waits for a body after `-X HEAD`) and drops `-i`.
   - `-X METHOD` when the method is not GET, or when a body is present (`--data-raw` alone would switch curl to POST).
   - `-H 'Name: value'` for each plain header, sorted by name for a stable output.
   - `--data-raw '<body>'` whenever the body is non-empty, matching `checker.go:294-302`.
   - `-L --max-redirs 10` unless `followRedirects` is `false`.
   - `-k` when `verifySsl` is `false`.
   - `--http2` for `"2"`, `--http3` for `"3"`, nothing for `"1.1"` or unset.
   - `--max-time <seconds>` when `config.timeout` parses as a Go duration (`500ms`, `10s`, `1m30s`). Omitted when unset or unparseable (the server default is not known to the client).
   - The URL last, quoted.
2. Secrets become placeholders, never values:
   - `basicAuth` in `configPrivateKeys`: `-u "$SOLIDPING_BASIC_AUTH"`, a real shell variable the operator exports as `user:pass`.
   - Legacy public `username` with `password` private: `-u 'alice:'"$SOLIDPING_BASIC_PASSWORD"`.
   - `secretHeaders` in `configPrivateKeys`: a leading comment line `# This check also sends secret headers that are not shown. Add each one with -H 'Name: value'.`
   - Any `${param:…}` or `${env:…}` text stays literal (single quotes keep the shell from touching it). When at least one is present, add a comment line `# ${param:…} and ${env:…} references are not resolved. Replace them before running.`
   - `redirectHostPolicy: same-host` has no curl equivalent: a comment line says so.
   - The default SolidPing User-Agent is not reproduced. A `User-Agent` set in `headers` is emitted like any other header.
3. Check detail page: add a `Copy as cURL` item to the more-actions menu (`checks.$checkUid.index.tsx:1428-1500`), shown only when `check.type === "http"`, with `data-testid="check-copy-curl"`. Handler mirrors `handleCopyLink` (`:1094-1108`): write to the clipboard, toast success. When any placeholder was emitted, the toast says secrets were replaced with placeholders.
4. Incident page: add a `Copy as cURL` button in the failure details card header (`web/dash0/src/routes/orgs/$org/incidents.$incidentUid.tsx:1460`, rendered at `:1235`), `data-testid="incident-copy-curl"`, for HTTP checks with a `checkUid`. Build it from `useCheck(org, incident.checkUid)` (`hooks.ts:925`), not from `incident.check.config`, because only the check endpoint returns `configPrivateKeys` and applies read-time redaction. The button tooltip says it uses the check's current config, which may differ from the config at failure time.
5. Locale keys in all four `web/dash0/src/locales/*/checks.json` and `incidents.json` (menu label, toasts, tooltip), so `locale-parity.test.ts` stays green.
6. Docs: one short paragraph under the HTTP section of `web/docs/docs/features/check-types.md` (`:8-60`): what the command contains and that secrets are placeholders.

## Tests
- `web/dash0/src/lib/curl-command.test.ts` (vitest):
  - GET with no body: no `-X`, `-i` present, URL last.
  - POST with body containing `'` and a newline: assert the exact escaped string (`'\''` for the quote).
  - GET with body: `-X GET` present.
  - HEAD: `--head`, no `-i`, no `-X`.
  - `followRedirects` unset: `-L --max-redirs 10`. `false`: neither flag.
  - `verifySsl: false`: `-k`. Unset: no `-k`.
  - `httpVersion` `"2"` / `"3"` / `"1.1"`.
  - `timeout` `"1m30s"` gives `--max-time 90`, `"500ms"` gives `--max-time 0.5`, unset or `"abc"` gives none.
  - `basicAuth` private: `$SOLIDPING_BASIC_AUTH` present. Legacy `username` public + `password` private: `'alice:'"$SOLIDPING_BASIC_PASSWORD"`.
  - `secretHeaders` private: comment line present. Negative: no `secretHeaders` key in output, and a config with no private keys has no comment line.
  - `${param:token}` in a header: kept literal, note line present. Negative: no note when no reference.
- `web/dash0/e2e/check-copy-curl.spec.ts`: create an HTTP check through the API with a plain header, a POST body, a secret header and a `basicAuth`. Open the detail page, open more actions, click `check-copy-curl`, read the clipboard (grant pattern of `e2e/check-detail.spec.ts:1276-1303`). Assert the URL, header and body are present and neither secret value appears anywhere in the text. Negative: a TCP check's menu has no `check-copy-curl`.

## To verify
- `web/dash0/src/components/checks/form/types/http.tsx:110` says the backend ignores a body on a GET probe, but `checker.go:294-302` builds the request with the body for any method. The builder follows the checker. If the comment is right, change the GET rule.
- How the checker negotiates `"2"` on a cleartext `http://` URL. If it uses prior knowledge, emit `--http2-prior-knowledge` there instead of `--http2`.

## Open questions
- Other check types (TCP through `nc`, DNS through `dig`)? Recommended: no. HTTP is the only type where a one-liner reproduces the probe faithfully.
- Expose secret header names (not values) from the API so the command can print `-H 'X-Api-Key: $SOLIDPING_SECRET_HEADER_1'`? Recommended: no. It widens what the read path returns for a convenience. The comment line is enough.
- Rewrite `${env:NAME}` into shell `$NAME`? Recommended: no. One rule for every reference is easier to read than a partial translation.
