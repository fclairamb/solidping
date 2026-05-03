# In-app bug reports → GitHub Issues (backend)

**Depends on:** spec #48 (Files storage foundation).

## Context

Today there's no path from "user noticed something wrong in the UI" to
"developer can fix it" — users have to guess where to file a report, and
they lose all of the diagnostic context (URL, viewport, browser, console
errors) along the way.

This spec adds a single endpoint that the dash0 / status0 frontends call
when a user submits a feedback dialog. The endpoint:

1. Stores the screenshot as a `File` (group `reports`) via spec #48.
2. Posts a GitHub issue to `fclairamb/solidping` with all the context
   embedded, including a 365-day signed URL to the screenshot so the issue
   is self-contained.

The frontend UI is spec #50.

## Honest opinion

1. **GitHub Issues is the right notification channel for now.** Email is
   fire-and-forget, has no status / assignment / search; spinning up our
   own incident-tracking surface for in-app reports would be massive
   over-build. Worst case the PAT is misconfigured and the report still
   lives as a `File` in the DB.
2. **The PAT belongs in env / config, not in the database.** It's a
   credential to a third-party service that gates "can this server post
   issues to fclairamb/solidping". Keeping it out of `parameters` keeps
   the blast radius of a DB compromise lower and prevents it from being
   accidentally exposed via the system-parameters API.
3. **`app.enable_bug_report` should auto-derive from "PAT is set + repo
   is set", not be a separate switch.** A separate boolean flag is one
   more way to misconfigure prod into a state where the icon shows up
   but submission fails. Compute it once at startup. Re-evaluate on
   parameter reload.
4. **GitHub issue creation runs async, in a goroutine.** The user-facing
   submission must return success the moment the `File` is persisted —
   the report is safe even if GitHub is down. We log the failure and
   move on.
5. **The `/mgmt/report` path stays public (no auth required).** A user
   reporting "I'm logged out and got an error" must be able to submit.
   We accept an optional bearer token to attribute the report to a user
   when present. This matches the de-facto pattern for in-app feedback
   widgets.
6. **There's no admin "list reports" UI in this spec.** Reports are
   visible on GitHub. The `File` row is for storage; it does not need
   its own dashboard.

## Scope

**In**

- `app.*` config keys for enabling and configuring the feature.
- `POST /mgmt/report` endpoint accepting `multipart/form-data` (matches
  the existing `/mgmt/*` pattern for diagnostic endpoints).
- Programmatic use of the `files` service (spec #48) to persist the
  screenshot.
- `GET /api/v1/features` minimal feature-flag endpoint that exposes
  `bug_report: bool` to the frontend (so the icon renders only when the
  feature is actually wired up — see spec #50).
- Background goroutine that creates the GitHub issue with embedded
  signed-URL screenshot.

**Out**

- A "list / triage" admin page for reports.
- Custom labels / templates / severity inference. Single label
  `in-app-report` is enough.
- Closing issues from the app (one-way flow).
- Email fallback when GitHub is down.
- Posting issues to anywhere other than GitHub (GitLab, Linear, etc.).

## Design

### 1. Configuration

Add to `config.Config`:

```go
type AppConfig struct {
    EnableBugReport bool `koanf:"enable_bug_report"` // computed; not user-set
    GitHub          AppGitHubConfig `koanf:"github"`
}

type AppGitHubConfig struct {
    IssuesToken string `koanf:"issues_token"` // fine-grained PAT, issues:write only
    Repo        string `koanf:"repo"`         // "fclairamb/solidping"
}
```

Defaults: `Repo: "fclairamb/solidping"`. `IssuesToken` is empty by
default.

`EnableBugReport` is **computed at config-load time** as
`IssuesToken != "" && Repo != ""`. We never read it from config /
env / DB directly. This eliminates the misconfiguration class
"icon visible but submit 500s".

Env vars (loaded in `config.go` alongside the existing pattern, since
underscores need manual handling):

- `SP_APP_GITHUB_ISSUES_TOKEN` (preferred) and `GITHUB_ISSUES_TOKEN`
  (compatibility / convenience for CI). Bare-name takes precedence
  only if `SP_*` is unset.
- `SP_APP_GITHUB_REPO`.

`systemconfig` registration:

- `app.github.issues_token` — secret, never returned via the system
  parameters API. Apply func sets `cfg.App.GitHub.IssuesToken` and
  recomputes `EnableBugReport`.
- `app.github.repo` — non-secret, same recompute.

### 2. `POST /mgmt/report`

Multipart form fields:

| Field         | Type    | Required | Notes                                          |
|---------------|---------|----------|------------------------------------------------|
| `url`         | string  | yes      | Page URL the user was on                       |
| `comment`     | string  | no       | Free-form description                          |
| `org`         | string  | no       | Org slug; defaults to first non-deleted org    |
| `annotations` | string  | no       | JSON-encoded array (rect/arrow/text)           |
| `context`     | string  | no       | JSON: viewport, UA, language, recent errors, … |
| `screenshot`  | file    | no       | image/png or video/webm                        |

Bearer token (optional) is read from `Authorization: Bearer ...`. If
valid → attributes the report to that user; otherwise silently dropped.

Body limit: 10 MB (matches the existing `maxMultipartMemory` pattern).

Response:

```json
{ "uid": "f3a8...-..." }
```

Errors: `BAD_REQUEST` (missing `url`), `REQUEST_TOO_LARGE`, `ORGANIZATION_NOT_FOUND`.

### 3. Service flow

`internal/handlers/feedback/service.go` — new package:

```go
type Service struct {
    db          *bun.DB
    files       *files.Service        // from spec #48
    auth        *auth.Service
    cfg         config.AppConfig
    baseURL     string
    jwtSecret   []byte
    httpClient  *http.Client
}

func (s *Service) SubmitReport(ctx context.Context, req SubmitReportRequest) (*SubmitReportResponse, error) {
    // 1. resolve org (by slug, fallback to first)
    // 2. resolve user (optional, from bearer token)
    // 3. if screenshot: files.CreateFile(... GroupTypeReports ...)
    // 4. insert/update a "report" record? — NO. The File row is the persistent record;
    //    additional report metadata (comment, url, annotations, context) lives in the
    //    GitHub issue body. Keeping a separate Report DB table is over-build for v1.
    // 5. if cfg.EnableBugReport: go s.createGitHubIssue(...)
    // 6. return { uid: file.UID } (or generated UID if no screenshot)
}
```

Note: we **do not** introduce a `reports` DB table. The user explicitly
wants a "very basic storage file concept", and the GitHub issue carries
the structured payload. If we later need offline triage we'll add it
then.

### 4. GitHub issue body

Title: `Bug report: <first 60 chars of comment>` (or `Bug report: <url path>`).

Body (markdown):

```markdown
## Report

{comment}

## Page

{url}

## Context

| Field | Value |
|-------|-------|
| Organization | {org_slug} |
| User | {user_email} |
| Server Version | {version} ({git_hash}) |
| Frontend Version | {build_version} |
| Browser | {user_agent} |
| Viewport | {w}x{h} |
| Pixel Ratio | {dpr} |
| Platform | {platform} |
| Language | {language} |
| Reported At | {iso8601} |

## Console Errors

```
{recent_errors_joined_with_newlines}
```

## Screenshot

![Screenshot]({signed_url})

<sub>File UID: `{uid}` · link valid until {iso8601}</sub>

---
*Auto-generated from in-app bug report*
```

For `video/webm` MIME the `## Screenshot` section becomes
`## Screen recording` and renders the bare URL on its own line (GitHub
auto-inlines a video player) plus a `[Download](url)` fallback link.

`signed_url` is built using `signedurl.Sign(jwtSecret, fileUID,
MaxSignedURLTTL)` from spec #48. The base URL is taken from the parsed
`req.URL` (so the link works for whoever opens the issue) and falls back
to `cfg.Server.BaseURL` if parsing fails.

Labels: `["in-app-report"]`.

### 5. `GET /api/v1/features`

Tiny auth-required endpoint that returns:

```json
{ "bugReport": true }
```

The frontend uses this to decide whether to render the bug-report icon.
This is preferable to bundling the flag into `/mgmt/version` (which is
public and unauthenticated) since later we'll want per-user feature
gating.

## Files affected

| File                                                          | Change                                                              |
|---------------------------------------------------------------|---------------------------------------------------------------------|
| `server/internal/config/config.go`                            | Add `AppConfig`, `AppGitHubConfig`, env loading, computed flag      |
| `server/internal/systemconfig/systemconfig.go`                | Register `app.github.*` parameters                                  |
| `server/internal/handlers/feedback/service.go`                | New service                                                         |
| `server/internal/handlers/feedback/handler.go`                | `POST /mgmt/report` handler                                         |
| `server/internal/handlers/feedback/issue_body.go`             | `buildIssueTitle` / `buildIssueBody` (split for readability+tests)  |
| `server/internal/handlers/feedback/service_test.go`           | Service-level tests                                                 |
| `server/internal/handlers/feedback/handler_test.go`           | Handler tests (multipart parsing, auth, errors)                     |
| `server/internal/handlers/feedback/issue_body_test.go`        | Body-builder branches: image / video / no-media                     |
| `server/internal/handlers/features/handler.go`                | `GET /api/v1/features`                                              |
| `server/internal/app/server.go`                               | Register `/mgmt/report` (public) and `/api/v1/features` (auth)      |
| `server/internal/app/services/services.go`                    | Wire feedback service                                               |
| `docs/api-specification.md`                                   | Document `/mgmt/report` and `/api/v1/features`                      |
| `CLAUDE.md` (root)                                            | Add the two endpoints to the API list                               |

## Tests

- **Config** — `EnableBugReport` true iff PAT+repo set; flips on / off
  when systemconfig parameter changes.
- **Issue body** — image, video, no-media branches; signed URL embedded
  round-trips through `signedurl.Verify`.
- **GitHub call** — using `httptest.NewServer` to assert the request URL,
  headers (`Authorization: Bearer ...`, `X-GitHub-Api-Version`,
  `Accept: application/vnd.github+json`), and JSON payload shape.
  Failure paths: 401 from GitHub logs and does **not** fail
  `SubmitReport` (the file is already saved).
- **Handler** — happy path returns 201 with `{uid}`; missing `url`
  returns 400; oversize multipart returns 413; bearer token attribution
  works.
- **Features endpoint** — returns `bugReport: true` only when the
  computed flag is on.

## Verification

1. `make test` — all pass.
2. Set `SP_APP_GITHUB_ISSUES_TOKEN` to a real fine-grained PAT (issues
   scope only) and `SP_APP_GITHUB_REPO=fclairamb/solidping`. Restart.
3. From the dash0 app (after spec #50 ships) submit a report. Confirm:
   - The screenshot lands in `data/files/<orgUid>/reports/...` (or in
     the configured S3 bucket).
   - A GitHub issue appears at https://github.com/fclairamb/solidping/issues
     with the screenshot inline, label `in-app-report`, and the context
     table populated.
   - The signed-URL footnote line is present and the URL fetches the
     image when opened in a private tab (so it works without auth).
4. **Negative**:
   - Unset the PAT → `/api/v1/features` returns `{bugReport: false}`,
     `POST /mgmt/report` still works (file saved) but logs
     `bug_report: github not configured` and posts no issue.
   - Tamper the signed URL on the issue → 403/410 from `/pub/files/...`.

## Security considerations

- The PAT is a fine-grained token with `issues:write` only on
  `fclairamb/solidping`. Worst case if leaked: spam issues. Document
  this in the PAT setup section of the deploy docs.
- Signed URLs in issue bodies are bearer tokens for one file. Don't log
  full URLs; the signedurl package log lines log only `{fileUid, exp}`.
- `/mgmt/report` is public. Add a per-IP rate limit (existing middleware
  if any; otherwise note as a follow-up — not blocking).
- The `recent_errors` payload from the frontend may include arbitrary
  strings. We embed it inside a fenced ``` code block so it can't break
  the markdown table or exfiltrate via crafted input.
