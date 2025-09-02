# Check Creation: URL Pre-fill Parameters & Protocol Auth Fields

## Overview

Two improvements to the check creation/editing UI:

1. **URL query parameters** on `/orgs/$org/checks/new` to pre-fill the form — enables shareable templates, external integrations, and bookmarkable configurations
2. **Username/password fields** for protocols that support authentication but lack UI controls

---

## Part 1: URL Query Parameters for Check Creation

### Route: `/orgs/$org/checks/new`

Add TanStack Router `validateSearch` to parse and apply query parameters as initial form values.

### Common Parameters

| Parameter | Type | Maps to | Example |
|-----------|------|---------|---------|
| `checkType` | CheckType enum | Type selector | `?checkType=http` |
| `checkPeriod` | number (seconds) | Period selector | `?checkPeriod=60` (1 minute) |
| `checkSlug` | string | Slug field | `?checkSlug=api-health` |
| `checkName` | string | Name field | `?checkName=API%20Health` |
| `checkGroup` | string (UID) | Group selector | `?checkGroup=abc123` |

`checkPeriod` is in seconds to keep URLs readable. Convert to `HH:MM:SS` format internally (e.g., `60` -> `"00:01:00"`, `3600` -> `"01:00:00"`).

### Per-Type Parameters

#### HTTP

| Parameter | Maps to | Example |
|-----------|---------|---------|
| `httpUrl` | URL field | `?httpUrl=https://api.example.com/health` |
| `httpMethod` | Method selector | `?httpMethod=POST` |
| `httpExpectedStatus` | Expected status | `?httpExpectedStatus=201` |

#### Host+Port types (TCP, UDP, SSH, POP3, IMAP, SMTP, FTP, SFTP, PostgreSQL)

| Parameter | Maps to | Example |
|-----------|---------|---------|
| `host` | Host field | `?host=db.example.com` |
| `port` | Port field | `?port=5432` |

#### URL-based types (SSL, WebSocket)

| Parameter | Maps to | Example |
|-----------|---------|---------|
| `url` | URL field | `?url=wss://example.com/ws` |

#### ICMP

| Parameter | Maps to | Example |
|-----------|---------|---------|
| `host` | Host field | `?host=8.8.8.8` |

#### DNS / Domain

| Parameter | Maps to | Example |
|-----------|---------|---------|
| `domain` | Domain field | `?domain=example.com` |

#### PostgreSQL extras

| Parameter | Maps to | Example |
|-----------|---------|---------|
| `database` | Database field | `?database=myapp` |

#### Auth fields (see Part 2 for which types get auth)

| Parameter | Maps to | Example |
|-----------|---------|---------|
| `username` | Username field | `?username=deploy` |

### Security: No passwords in URLs

**Passwords MUST NOT be accepted as query parameters.** They would appear in browser history, server logs, analytics, Referer headers, and shared links. The `password` field is always filled manually.

### Example URLs

```
# Pre-fill an HTTP check
/orgs/default/checks/new?checkType=http&httpMethod=GET&httpUrl=https://api.example.com/health&checkPeriod=30

# Pre-fill a PostgreSQL check
/orgs/default/checks/new?checkType=postgresql&host=db.prod.internal&port=5432&username=monitor&database=app

# Pre-fill an SSH check
/orgs/default/checks/new?checkType=ssh&host=bastion.example.com&port=22&username=healthcheck

# Pre-fill a DNS check
/orgs/default/checks/new?checkType=dns&domain=example.com
```

### Implementation

In `checks.new.tsx`, add `validateSearch`:

```typescript
import { z } from "zod";

const checkNewSearchSchema = z.object({
  checkType: z.enum(["http", "tcp", ...]).optional(),
  checkPeriod: z.number().positive().optional(),
  checkSlug: z.string().optional(),
  checkName: z.string().optional(),
  checkGroup: z.string().optional(),
  // Per-type
  httpUrl: z.string().optional(),
  httpMethod: z.enum(["GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"]).optional(),
  httpExpectedStatus: z.number().optional(),
  host: z.string().optional(),
  port: z.number().min(1).max(65535).optional(),
  url: z.string().optional(),
  domain: z.string().optional(),
  username: z.string().optional(),
  database: z.string().optional(),
});

export const Route = createFileRoute("/orgs/$org/checks/new")({
  validateSearch: checkNewSearchSchema,
  component: NewCheckPage,
});
```

Pass the search params as `initialData` to `CheckForm`, constructing the appropriate shape.

---

## Part 2: Username/Password Fields per Protocol

### Current State

| Protocol | Backend has user/pass | UI has user/pass | Action needed |
|----------|----------------------|------------------|---------------|
| HTTP | No (auth via `headers` map only) | No | **Add `username`/`password` backend fields + UI** |
| FTP | Yes | Yes | None |
| SFTP | Yes | Yes | None |
| SSH | Yes (`username`, `password`, `private_key`) | **No** | **Add UI fields** |
| SMTP | No (only `check_auth` flag) | No | **Add backend + UI** |
| IMAP | No | No | **Add backend + UI** |
| POP3 | No | No | **Add backend + UI** |
| PostgreSQL | Yes | Yes | None |

### Phase 1: SSH (frontend-only)

The SSH backend already supports `username`, `password`, and `private_key`. The UI just doesn't render them.

Add username/password fields to the SSH form section. SSH checks currently share the `tcp/udp/ssh/pop3/imap` case — extract SSH into its own case to add auth fields.

```
Host: [____________] Port: [22]
Username (optional): [____________]
Password (optional): [____________]
```

The `username` and `password` state variables already exist in CheckForm (used by FTP/SFTP/PostgreSQL). Just wire them into the SSH submit handler too (line ~237: add username/password to config for SSH).

### Phase 2: HTTP Auth (backend + frontend)

Add explicit `username` and `password` fields to `HTTPConfig`. These are **independent** of the `headers` map — both can be set simultaneously (e.g., Basic Auth via user/pass + a custom `X-API-Key` header).

**Backend changes** (`back/internal/checkers/checkhttp/`):

Add fields to `HTTPConfig`:

```go
Username string `json:"username,omitempty"`
Password string `json:"password,omitempty"`
```

Behavior:
- If `username` is set, add a `Basic` Authorization header: `base64(username + ":" + password)`
- Applied **before** the `headers` map, so an explicit `Authorization` in `headers` overrides it
- `password` can be empty (some APIs use username-only auth)

Add `FromMap` / `GetConfig` / `Validate` support following the same pattern as other string fields.

**Frontend changes**: Add username/password fields to the HTTP form section (below Method + URL, above Expected Status):

```
Request:  [GET v] [https://example.com_________]
Username (optional): [____________]
Password (optional): [____________]
Expected Status: [200]  Check Interval: [1 minute v]
```

### Phase 3: SMTP AUTH (backend + frontend)

**Backend changes** (`back/internal/checkers/checksmtp/`):

Add fields to `SMTPConfig`:

```go
Username string `json:"username,omitempty"`
Password string `json:"password,omitempty"`
```

Behavior:
- If `username` is set, attempt SMTP AUTH after EHLO/STARTTLS using `smtp.Auth` (PLAIN mechanism, falling back to LOGIN)
- If auth fails, the check is DOWN
- The existing `check_auth` flag remains: it only *verifies AUTH is advertised*, it doesn't authenticate

**Frontend changes**: Add username/password fields to the SMTP form section (below existing TLS/EHLO fields).

### Phase 4: IMAP AUTH (backend + frontend)

**Backend changes** (`back/internal/checkers/checkimap/`):

Add fields to `IMAPConfig`:

```go
Username string `json:"username,omitempty"`
Password string `json:"password,omitempty"`
```

Behavior:
- After connecting and optional STARTTLS, send `LOGIN username password` command
- Verify response starts with the command tag followed by `OK`
- If login fails, the check is DOWN

**Frontend changes**: Add username/password fields to the IMAP form section.

### Phase 5: POP3 AUTH (backend + frontend)

**Backend changes** (`back/internal/checkers/checkpop3/`):

Add fields to `POP3Config`:

```go
Username string `json:"username,omitempty"`
Password string `json:"password,omitempty"`
```

Behavior:
- After connecting and optional STARTTLS, send `USER username` then `PASS password`
- Verify `+OK` responses
- If auth fails, the check is DOWN

**Frontend changes**: Add username/password fields to the POP3 form section.

---

## Frontend Changes Summary

### CheckForm component changes

1. **Accept `searchParams` prop** (from route search params) for pre-filling
2. **Extract SSH into its own form section** with host/port + username/password
3. **Add HTTP auth section** (collapsible, None/Basic/Bearer)
4. **Add username/password to SMTP/IMAP/POP3** form sections
5. **Wire username/password into submit handler** for SSH, SMTP, IMAP, POP3

### Form section layout after changes

| Type | Fields |
|------|--------|
| HTTP | Method + URL, **Username, Password**, Expected Status, Period |
| TCP | Host + Port |
| UDP | Host + Port |
| SSH | Host + Port, **Username, Password** |
| ICMP | Host |
| DNS | Domain |
| Domain | Domain |
| SSL | URL |
| WebSocket | URL |
| SMTP | Host + Port, STARTTLS, TLS Verify, Check AUTH, EHLO Domain, Expected Greeting, **Username, Password** |
| POP3 | Host + Port, **Username, Password** |
| IMAP | Host + Port, **Username, Password** |
| PostgreSQL | Host + Port, Username, Password, Database, Query |
| FTP | Host + Port, Username, Password |
| SFTP | Host + Port, Username, Password |
| JS | Script |
| Heartbeat | (none) |

---

## Implementation Order

1. **URL query params** — frontend-only, no backend changes
2. **SSH auth UI** — frontend-only, backend already supports it
3. **HTTP auth** — backend (`username`/`password` fields) + frontend
4. **SMTP/IMAP/POP3 auth** — backend + frontend, can be done together or individually

Phases 1-2 are frontend-only. Phases 3-4 require backend changes (one PR per protocol is fine).

---

## Out of Scope

- Private key upload UI for SSH/SFTP (complex UX, low priority)
- Bearer token / OAuth / API key UI for HTTP (can be done via the existing `headers` map)
- TLS settings UI for POP3/IMAP (backend supports them but low priority for initial auth work)
- Password storage encryption (should be addressed as a separate security feature if not already handled)
