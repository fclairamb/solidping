---
model: sonnet
effort: medium
---

# Incident notifications never say which region saw the failure, so a regional outage reads like a global one

## Problem
An on-call who gets a SolidPing alert cannot tell whether the service is down for everyone or only seen as down from one probe location. They have to open the dashboard to find out.

- The Slack card shows Monitor, Cause and Check (method + URL) and nothing else (`server/internal/notifications/slack.go:549-561`, in `buildIncidentFields`). The other senders render the same facts.
- The webhook payload carries `incident` and `check` only (`server/internal/notifications/webhook.go:54-96`, built in `buildPayload` at `webhook.go:401-445`). The documented shape has no region either (`web/docs/docs/configuration/notifications.md:911-946`).

The data already exists, it is just never passed to the senders:
- At open, the incident stores a snapshot of the triggering result, including its region: `first_result.region` (`server/internal/handlers/incidents/failure_details.go:37`, written by `failureDetails` at `failure_details.go:68`, called from `createIncident` at `handlers/incidents/service.go:1481`). A relapse adds `last_failure` (`failure_details.go:101-107`).
- Every check with 2 or more regions keeps one state row per region (`handlers/incidents/quorum.go:86`). `regionquorum.Evaluate` splits the regions into Failing / Passing / Unknown (`server/internal/regionquorum/regionquorum.go:269-336`). `deriveSignal` computes that evaluation for every result in quorum mode (`quorum.go:124-166`), then drops it once the signal is known.
- With a fail quorum (spec 2026-09-25-10), "2 of 3 regions failing" is what tells you it is the service and not one network path.

## Proposal
1. **Snapshot the regions on the incident.** Add a `regions` key to `incident.Details`, named like the checks API block `RegionalIssueResponse` (`server/internal/handlers/checks/fail_quorum.go:147-155`): `{"failingRegions": ["eu-west", "us-east"], "regionCount": 3, "failQuorum": 2}`.
   - Write it in `createIncident` (`service.go:1481`) and on reopen, next to `lastFailureDetails` (`failure_details.go:101`). Only write it when `regionquorum.RegionCount(check) >= 2`.
   - In quorum mode, reuse the evaluation `deriveSignal` already computed: add the `Failing` slice (or the whole `Evaluation`) to `resultSignal` (`quorum.go:31-44`). That avoids a second query.
   - When the check has several regions but is not in quorum mode (`all`, or `default` with 2 regions, `regionquorum.go:171-178`), call `s.evaluateQuorum` (`quorum.go:174-188`) once. That is one `ListCheckRegionStates` query per incident open, not per result.
   - A single-region or passive check gets no `regions` key. Senders fall back to `first_result.region`.
2. **Refresh the snapshot on escalation.** In the escalation branch of `handleFailure` (`service.go:1357-1371`), which already writes the incident row, rewrite `regions` from the current signal, since more regions may have failed by then. Do not refresh on resolve: the resolved message reports which regions had failed.
3. **Add one shared helper** in a new `server/internal/notifications/regions.go`, following the `BurnInfoFor` / `DegradedInfoFor` pattern (`notifications/slo_burn.go:62`, `notifications/degraded.go:74`).
   - `RegionInfoFor(incident)` reads `Details.regions` first. If it is missing, it falls back to `first_result.region` (or `last_failure.region` after a relapse). It returns nil when there is no region.
   - `Text(labels)` renders `🇪🇺 Europe West, 🇺🇸 US East (2 of 3 regions)`. With one known region it renders just the label, with no "1 of 1".
4. **Use dashboard labels.** dash0 shows `{emoji} {name}` from the region definitions and falls back to the raw slug (`web/dash0/src/lib/region-label.ts:8-14`). Do the same:
   - Resolve the labels at send time in `NotificationJobRun.buildPayload` (`server/internal/jobs/jobtypes/job_notification.go:212-243`) through `regions.Service.GetGlobalRegions` (`server/internal/regions/regions.go:172`) and `GetOrgCustomRegions` (`regions.go:351`), so org-private regions resolve too.
   - Carry the result as a new `Regions *RegionInfo` field on `Payload` (`notifications/sender.go:16`).
   - If the labels fail to load, log it and use the slugs. Never fail the notification over this.
5. **Add one line per sender** for `incident.created`, `incident.reopened`, `incident.escalated` (and `incident.reminder` if spec 2026-10-12-16 has landed): "Failing from: ...". `incident.resolved` gets "Failed from: ...".
   - Comment, ack and unack follow-ups get no region line.
   - Burn and degraded incidents keep their own field sets (`slack.go:522-547`) and get no region line.
   - Files: `slack.go` (new field in `buildIncidentFields`, reused by `slackwebhook.go:105-110`), `discord.go` (embed fields, also used by `discord_webhook.go`), `msteams.go`, `msteamsbot.go`, `googlechat.go`, `mattermost.go`, `matrix.go`, `zulip.go` (`buildContent`, `zulip.go:218`), `gotify.go`, `ntfy.go`, `pushover.go`, `webpush.go`, `pagerduty.go` (`custom_details`, `pagerduty.go:174`), `twilio.go` (`buildBody`, `twilio.go:103-105`), and `email.go` (`buildIncidentViewModel`, `email.go:637`) with the `email/templates/incident-{created,reopened,escalated,resolved}.html` templates.
   - Twilio uses the region name without the emoji. One emoji forces UCS-2 and halves the SMS length.
6. **Add a webhook field.** Add `Regions *WebhookRegions` with `json:"regions,omitempty"` to `WebhookIncident` (`webhook.go:76-89`), with fields `failingRegions` (slugs, not labels: machines consume this), `regionCount` and `failQuorum`.
   - A single-region check gives `{"failingRegions": ["eu-west"], "regionCount": 1, "failQuorum": 1}`.
   - The field is omitted when no region is known. This is additive, so existing receivers keep working.
7. **Docs.** Add `regions` to the example in `web/docs/docs/configuration/notifications.md:913-936`, plus a bullet in the list under it (`notifications.md:938-946`) saying it holds slugs, is omitted when unknown, and is a snapshot taken at open and at escalation. Add a `CHANGELOG.md` entry per `wiki/conventions/changelog.md`.
8. **Out of scope:** probe egress IPs (no model or column records them today), live per-region state at send time (the snapshot is the source), and public status pages (they never read `incident.Details`).

## Tests
- `server/internal/handlers/incidents/quorum_test.go`:
  - 3 regions with Q=2: the incident opens with `regions.failingRegions` holding the 2 failing slugs, `regionCount` 3 and `failQuorum` 2.
  - 2 regions with the `all` quorum: the incident stores both regions.
  - Single region: the incident has no `regions` key.
  - Escalation rewrites the snapshot after a third region fails.
  - Positive control: the region-state query runs once per open, not once per result.
- `server/internal/notifications/regions_test.go` (new), table-driven:
  - The snapshot renders "a, b (2 of 3 regions)".
  - Only `first_result.region` renders "a" with no "of".
  - A relapse uses `last_failure.region`.
  - No region returns nil.
  - An unknown slug falls back to the slug.
  - A burn or degraded incident returns nil.
- Sender tests, one multi-region case and one no-region case each (the repo asserts on built messages, there are no golden files): `slack_test.go`, `slackwebhook_test.go`, `discord_webhook_test.go`, `msteams_test.go`, `msteamsbot_test.go`, `googlechat_test.go`, `mattermost_test.go`, `matrix_test.go`, `zulip_test.go`, `gotify_test.go`, `pushover_test.go`, `webpush_test.go`, `pagerduty_test.go`, `twilio_test.go` (no emoji in the body), `email_test.go`. The no-region case asserts the line is absent, not empty.
- `server/internal/notifications/webhook_test.go`:
  - `data.incident.regions` holds slugs, `regionCount` and `failQuorum`.
  - The key is absent from the JSON when nothing is known.
  - All existing keys are unchanged.
- `server/internal/jobs/jobtypes` (notification job test): labels come from org custom regions. A missing regions parameter falls back to slugs and still sends.
- `make test` and `~/go/bin/golangci-lint run ./...` from `server/`.

## To verify
- `ntfy.go` has no `ntfy_test.go` today. Create it for the region case.
- Check whether a webhook payload JSON schema lives anywhere besides the docs (OpenAPI, `wiki/`), and update it if it does.

## Open questions
- Should `incident.resolved` carry the region line? Recommended: yes, in past tense ("Failed from"). It confirms which path recovered and costs one line.
