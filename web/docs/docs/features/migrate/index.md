---
sidebar_position: 0
title: Overview
description: Import your existing monitors into SolidPing from Gatus, Better Stack, Uptime Kuma or UptimeRobot.
---

# Migrating to SolidPing

You should not have to recreate your monitoring by hand to try something else.
SolidPing reads the export your current tool already produces — a config file, a
backup, an API response — and converts every monitor into a SolidPing check,
with its type, interval, target and thresholds intact.

## Supported sources

| Source | What you provide | Guide |
|---|---|---|
| Gatus | `config.yaml` | [Migrate from Gatus](./from-gatus.md) |
| Better Stack | An API token | [Migrate from Better Stack](./from-better-stack.md) |
| Uptime Kuma | A 1.x backup JSON | [Migrate from Uptime Kuma](./from-uptime-kuma.md) |
| UptimeRobot | The API v2 `getMonitors` JSON | [Migrate from UptimeRobot](./from-uptimerobot.md) |

## Recommended: let an AI agent do the translation

The built-in importers map field to field. That is what you want for the common
case, but it means anything without a direct SolidPing equivalent comes back as
a warning rather than a decision — and the settings most specific to your
organization are the ones most likely to land there.

An AI assistant does not have that limitation. Point Claude, Gemini, Codex or
any other capable agent at both your existing setup and SolidPing, and it can
read your conventions — how you name things, how you group them, what a given
threshold or hand-rolled probe was actually protecting — and pick the closest
SolidPing equivalent instead of dropping it. This is the recommended route when:

- your tool is not in the table above;
- your configuration is templated, generated, or spread across many files;
- you want to reorganize as you move — into
  [check groups](/docs/features/check-groups), [SLOs](/docs/features/slos) or
  [status pages](/docs/features/status-pages) — rather than land a flat copy of
  what you already had.

### Give the agent access

The quickest route is the built-in [MCP server](/docs/features/mcp). An
MCP-capable client needs only the URL:

```
{SP_BASE_URL}/api/v1/mcp
```

That hands the agent the tools this job actually needs: list the available check
types, fetch a sample config for each, **validate** a candidate check before
anything is written, then create it. An agent without MCP support can work
straight against the [REST API](/docs/api/solidping-api) instead.

Then give it your export and let it work:

> Here is my Gatus `config.yaml`. Using the SolidPing MCP server, list the
> available check types, work out the closest equivalent for each endpoint —
> including the ones that do not map cleanly — validate each one, and show me
> what you plan to create before you create it.

### Review what it produces

An agent is making judgment calls, so keep the discipline the importers enforce
for you: have it validate and show you the plan first, then read what it
created. Two things make this cheap to iterate on — checks are matched by slug,
so a corrected re-run updates in place instead of duplicating, and a token
scoped to `mcp:read` lets an agent survey your instance with no ability to write
anything at all.

## How an import works

Every source goes through the same path, so the mechanics below hold whichever
guide you follow.

1. Open **Checks** in the dashboard and click **Import**.
2. Pick your source and paste the file, token or JSON.
3. Click **Import preview**. **Nothing is written yet** — you get the exact list
   of checks that would be created and updated, plus every item that did not map
   cleanly.
4. Read the warnings, then confirm.

The preview is the same call as the real import with `dryRun=true`, so what you
review is what you get — not a separate estimate.

### Re-importing is safe

Checks are matched by slug and updated in place, never duplicated. You can run
the import again as your old configuration changes, and keep both tools running
side by side until you are ready to cut over.

Every imported check carries the label `solidping.io/managed=<source>` (for
example `solidping.io/managed=gatus`), so you can filter on exactly what the
import created.

### What is never imported

- **Secrets** — SSH credentials, API keys and passwords found in a foreign
  config are deliberately dropped rather than silently re-persisted. Re-enter
  them on the check.
- **Notification bindings** — alert contacts, escalation policies and on-call
  rotations do not carry over. Set up SolidPing
  [integrations](/docs/features/incidents) and [on-call](/docs/features/on-call)
  after the import.
- **Status history** — monitoring results are not portable between tools.

Anything else that has no SolidPing equivalent is reported as a warning on the
preview; in most cases the check is still imported, minus that one setting. Each
guide lists what its source leaves behind.

## Via the API

Every importer is the same endpoint, with the source as a query parameter:

```bash
curl -s -X POST \
  -H "Authorization: Bearer $TOKEN" \
  --data-binary @your-export-file \
  'https://your-instance/api/v1/orgs/myorg/checks/import/convert?source=gatus&dryRun=true' | jq '.'
```

Valid `source` values are `gatus`, `betterstack`, `uptime-kuma` and
`uptimerobot`. Drop `dryRun=true` to apply. The response carries the `created`
and `updated` counts and the full warning list.

## Your tool is not listed?

This is where an [AI agent](#recommended-let-an-ai-agent-do-the-translation)
earns its keep — it needs no importer, only your export and access to the MCP
server. Failing that, the importers are thin converters onto the normal check
API, so anything you can export as structured data can be scripted against
[`POST /api/v1/orgs/:org/checks`](/docs/api/solidping-api) directly. If you would like a
first-class importer for another tool,
[open an issue](https://github.com/fclairamb/solidping/issues) with a sample
export — that sample is most of the work.
