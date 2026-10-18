# Public check tools

The anonymous "run a check once" endpoint behind the public tools of www.solidping.io
(website down checker, SSL checker, DNS lookup, port checker). Spec 2026-10-14-02. Code:
`server/internal/handlers/tools`.

### POST /api/v1/tools/run
Run one `http` (GET), `ssl`, `dns` or `tcp` check once. Nothing is persisted: no check, result,
incident or notification. Auth: public. `404 NOT_FOUND` unless `tools.enabled` /
`SP_TOOLS_ENABLED=true`.

```json
{ "type": "dns", "target": "example.com", "options": { "record": "MX" } }
```

| Type | Target | Options |
|---|---|---|
| `http` | a URL, `https://` assumed without a scheme; no credentials | none |
| `ssl` | `host`, `host:port` or a URL; port 443 by default | none |
| `tcp` | `host:port`, port 1 to 65535 | none |
| `dns` | a domain name (not an IP) | `record`: A (default), AAAA, CNAME, MX, TXT, NS, CAA |

Response:

```json
{
  "type": "ssl",
  "target": "example.com:443",
  "status": "up",
  "durationMs": 182,
  "checkedAt": "2026-10-14T10:00:00Z",
  "region": "eu-paris",
  "cached": false,
  "details": { "issuer": "R3", "notAfter": "...", "daysLeft": 41, "sans": ["..."], "chainValid": true, "tlsVersion": "TLS 1.3" },
  "monitor": { "checkType": "ssl", "query": "checkName=example.com&checkType=ssl&host=example.com&port=443" }
}
```

`details` per type: `http` gives `statusCode`, `finalUrl`, `redirects`, `responseTimeMs`,
`httpProtocol`, and `certNotAfter` / `certDaysLeft` over HTTPS. `ssl` gives `issuer`, `subject`,
`notBefore`, `notAfter`, `daysLeft`, `sans`, `chainValid`, `tlsVersion`. `dns` gives `record`
and `records`. `tcp` gives `connected` and `connectTimeMs`. `error` carries the failure reason
when there is one. `monitor.query` prefills `/d/orgs/:org/checks/new`.

Errors (standard shape, see [errors.md](errors.md)):

| Status | Code | When |
|---|---|---|
| 400 | `VALIDATION_ERROR` | body is not JSON |
| 400 | `INVALID_TARGET` | unknown type, malformed target, port 0 or above 65535, unsupported record |
| 403 | `TARGET_NOT_ALLOWED` | a non-public address, a name with any non-public answer, a redirect hop to one, or the instance's own hosts |
| 422 | `RESOLVE_FAILED` | the name does not resolve |
| 429 | `RATE_LIMITED` | more than 10 runs a minute or 100 a day from one IP |
| 503 | `SERVICE_BUSY` | 16 runs already in flight; `Retry-After: 5` |
| 504 | `TIMEOUT` | no answer within 10 s |

Guard: the run uses an egress guard hardwired to deny private targets. It ignores
`SP_EGRESS_ALLOW_PRIVATE`. `ssl` and `tcp` dial the address that was vetted; `http` dials
through the guarded transport, which re-resolves and re-checks every hop (5 redirects at most).
`http` never reads the response body. `dns` asks 1.1.1.1 (then 8.8.8.8), never the system
resolver. It refuses single-label names and internal zones (`.local`, `.internal`,
`.cluster.local`, `.localhost`, `.localdomain`, `.home.arpa`, `.lan`) before the query, and any
answer holding a non-public address or such a name after it (`TARGET_NOT_ALLOWED`).
The instance's own hosts are the base URL host, the docs host and `SP_TOOLS_DENIED_HOSTS`
(subdomains included).

Results are cached 60 s per (type, target, options); concurrent identical runs share one
request. CORS is public (`Access-Control-Allow-Origin: *`). PostHog gets `tool_run` with
`type`, `status` and `cached` only. Logs carry a salted hash of the client IP, never the IP.
