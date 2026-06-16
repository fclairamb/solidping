# Slack notifications: add dashboard hyperlinks

## Context

Slack Block Kit mrkdwn supports inline hyperlinks via `<URL|display text>`. The current Slack
notification messages show the monitor name and status tags as plain text even though the
SolidPing dashboard has direct pages for both the check and the incident.

As visible in the screenshot, the "Monitor" label and "✅ Resolved 🔵 Monitor" context tags are
unlinked. Users have to navigate manually to find the incident or check.

The Payload struct already carries `OrgSlug` (populated in
`server/internal/jobs/jobtypes/job_notification.go:160`). The application base URL is
available via `jctx.AppConfig.Server.BaseURL` (same pattern used by the email sender in
`server/internal/notifications/email.go:235`).

Dashboard routes:
- Check detail: `{BaseURL}/dash0/orgs/{orgSlug}/checks/{checkSlug}`
- Incident detail: `{BaseURL}/dash0/orgs/{orgSlug}/incidents/{incidentUID}`

## Goals

Add hyperlinks to every Slack message wherever there is something meaningful to link to:

1. **Monitor field** — change `*Monitor:*\n{checkName}` to `*Monitor:*\n<{checkURL}|{checkName}>`.
   Applies to all message types that include this field
   (`buildIncidentFields`, `buildResolvedUpdateMessage`, `buildReopenedUpdateMessage`).

2. **Context "Monitor" label** — change `:large_blue_circle: Monitor` to
   `<{checkURL}|:large_blue_circle: Monitor>` (wraps the entire emoji+word so the link is obvious).
   Applied in all context blocks that reference Monitor
   (`buildIncidentCreatedBlocks`, `buildResolvedUpdateMessage`, `buildReopenedUpdateMessage`).

3. **Context "Incident" label** — change `:warning: Incident` / `:white_check_mark: Resolved` etc.
   to `<{incidentURL}|:warning: Incident>` etc., giving recipients a one-click link to the
   incident detail page.

## Non-goals

- Adding links to the push-notification title text (`Text` / `Fallback`) — those are plain-text
  fallback strings and Slack does not render mrkdwn there.
- Linking the raw "Checked URL" field (`*Checked URL:*\nGET \`https://...\``) — that URL is the
  _monitored endpoint_, not a SolidPing page; it stays as-is.
- Any frontend changes.

## Implementation plan

### 1. Propagate base URL into the Slack sender

Add a `AppBaseURL` string field to `notifications.Payload`
(`server/internal/notifications/sender.go`). Set it alongside `OrgSlug` in
`job_notification.go`:

```go
payload := &notifications.Payload{
    ...
    OrgSlug:    orgSlug,
    AppBaseURL: jctx.AppConfig.Server.BaseURL,
}
```

### 2. Add URL helpers in `slack.go`

Add two small functions to `server/internal/notifications/slack.go`:

```go
func checkDashURL(baseURL, orgSlug string, check *models.Check) string {
    if baseURL == "" || orgSlug == "" || check == nil || check.Slug == nil {
        return ""
    }
    return fmt.Sprintf("%s/dash0/orgs/%s/checks/%s", baseURL, orgSlug, *check.Slug)
}

func incidentDashURL(baseURL, orgSlug string, incident *models.Incident) string {
    if baseURL == "" || orgSlug == "" || incident == nil || incident.UID == "" {
        return ""
    }
    return fmt.Sprintf("%s/dash0/orgs/%s/incidents/%s", baseURL, orgSlug, incident.UID)
}
```

Add a helper that wraps text in a Slack link when a URL is available, falling back to plain text:

```go
func slackLink(url, text string) string {
    if url == "" {
        return text
    }
    return fmt.Sprintf("<%s|%s>", url, text)
}
```

### 3. Thread URL-linked text through all build functions

Update the signatures of `buildIncidentFields`, `buildIncidentCreatedBlocks`,
`buildResolvedUpdateMessage`, and `buildReopenedUpdateMessage` to accept `checkURL` and
`incidentURL` strings, computed once at the top of the respective entry-point builders
(`buildIncidentCreatedMessage`, `buildResolvedUpdateMessage`, `buildReopenedUpdateMessage`,
`buildIncidentEscalatedMessage`):

```go
checkURL    := checkDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Check)
incidentURL := incidentDashURL(payload.AppBaseURL, payload.OrgSlug, payload.Incident)
```

Then replace plain-text monitor names and context tags:

- `"*Monitor:*\n" + checkName` → `"*Monitor:*\n" + slackLink(checkURL, checkName)`
- `":large_blue_circle: Monitor"` → `slackLink(checkURL, ":large_blue_circle: Monitor")`
- `":warning: Incident"` → `slackLink(incidentURL, ":warning: Incident")`
- `":white_check_mark: Resolved"` → `slackLink(incidentURL, ":white_check_mark: Resolved")`
- `":rotating_light: Escalated"` → `slackLink(incidentURL, ":rotating_light: Escalated")`
- `":warning: Reopened"` → `slackLink(incidentURL, ":warning: Reopened")`

All changes are additive: when `AppBaseURL` or `OrgSlug` are empty (e.g. org lookup failed),
`slackLink` returns the plain text unchanged — no regression.

### 4. Tests

Update the existing Slack tests in `server/internal/notifications/slack_test.go` to set
`AppBaseURL` and `OrgSlug` on the payload and assert that the generated blocks contain the
expected `<https://...>` link patterns for the monitor name, Monitor context tag, and Incident
context tag.

Add a test case where `AppBaseURL` is empty, verifying fallback to plain text.

## Verification

1. Trigger a test incident against a dev SolidPing instance.
2. In the Slack notification, click the monitor name → lands on the check detail page.
3. Click the ":warning: Incident" context tag → lands on the incident detail page.
4. Click the ":large_blue_circle: Monitor" context tag → lands on the check detail page.
5. `make test` passes.
6. `make lint` passes.
