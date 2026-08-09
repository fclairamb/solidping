---
sidebar_position: 5
title: CLI Client
---

# CLI Client

SolidPing ships a command-line client, `sp`, for managing your monitoring from the terminal or from scripts and CI pipelines. It talks to the same REST API as the dashboard, so anything you can do in the UI you can also automate.

## Authentication

Log in once and the client stores your session:

```bash
sp auth login                  # device flow: approve a one-time code in any browser
sp auth login --with-password  # classic email + password
sp auth login --token pat_...  # save a Personal Access Token created in the dashboard
sp auth me                     # show the current user
sp auth switch-org             # change active organization
sp auth logout
```

`sp auth login` uses the OAuth 2.0 Device Authorization Grant (RFC 8628): it
prints a short one-time code and a verification URL, tries to open your browser
at that URL, and waits while you approve the login in any browser where you are
already signed in — including your phone. Because nothing has to come back to
the machine running the CLI, this works over SSH, inside containers and on
headless servers.

On the consent page you pick which organization the login is for (when you
belong to more than one); approving mints a named Personal Access Token scoped
to that organization, valid for 90 days, which you can review and revoke from
**Account → Tokens**.

## Common Commands

| Command | Description |
|---------|-------------|
| `sp checks list` | List checks |
| `sp checks get <uid>` | Show a check |
| `sp checks add` / `update` / `upsert` | Create or modify checks |
| `sp checks deps` | Manage check dependencies |
| `sp checks remove <uid>` | Delete a check |
| `sp checks export` / `import` / `diff` / `validate` | Config-as-code: see below |
| `sp results list` | List check results (with filters) |
| `sp incidents list` / `get` | Inspect incidents and their events |
| `sp events list` | Browse the audit event log |
| `sp tokens list` / `create` / `revoke` | Manage API tokens |
| `sp members list` / `add` / `update` / `remove` | Manage organization members |
| `sp jobs list` / `get` / `create` / `cancel` | Manage background jobs |
| `sp system get` / `set` / `delete` | Read and write system parameters |
| `sp server health` / `version` | Check server status |

## Config as Code

`sp checks` supports a full export → edit → validate → dry-run → import → re-export loop for managing a whole organization's checks as one tracked YAML (or JSON) file — the same shape the dashboard's export produces (`{version, exportedAt, organization, secrets, defaults, checks}`).

```bash
# 1. Export the current state (writes YAML because of the .yaml extension;
#    pass --format explicitly to override, or write .json for the raw JSON).
sp checks export --file config.yaml

# 2. Edit config.yaml by hand (or with a script) — add/rename/tune checks.

# 3. Validate the file offline: no token, no network call. Checks document
#    shape, slug/config formats, and the dependency graph.
sp checks validate config.yaml

# 4. Preview what would change against the live organization.
sp checks import config.yaml --dry-run

# 5. Apply it for real.
sp checks import config.yaml

# 6. Re-export and commit, so the tracked file matches live state again.
sp checks export --file config.yaml
```

`sp checks diff config.yaml` reports whether the tracked file has drifted from what SolidPing currently holds — useful as a CI check after every merge. It exits `0` when there's no drift, `1` when the file and the live org disagree, and `2`+ on errors (missing file, auth failure, ...); the `exportedAt` timestamp is ignored on both sides since it always differs.

**Import never deletes.** `sp checks import` is an idempotent upsert keyed on each check's `slug`: a check present in SolidPing but absent from the file is left untouched. If a check was removed from the file on purpose, delete it explicitly with `sp checks remove`, or use `sp apply --prune` (a separate, declarative-reconcile command) for delete-by-absence semantics. Always start from a fresh `sp checks export` before hand-editing so the file reflects live state.

`sp checks export` picks its output format from `--format yaml|json`, defaulting to the `--file` extension (`.yaml`/`.yml` → YAML, everything else including stdout → JSON). YAML output preserves the document's field order — two exports of unchanged live state produce byte-identical files, so diffs in version control only ever show real changes.

## Output Formats

The client can print human-readable tables or machine-readable output for scripting:

```bash
sp checks list --output json
sp results list --output jsonl
```

## Configuration

The client is configured through `SOLIDPING_`-prefixed environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `SOLIDPING_CONFIG` | `~/.config/solidping/settings.json` | Config / session file path |
| `SOLIDPING_URL` | - | Server URL override |
| `SOLIDPING_ORG` | - | Organization override |
| `SOLIDPING_OUTPUT` | `text` | Output format: `text`, `json`, `jsonl` |
| `SOLIDPING_VERBOSE` | `false` | Verbose logging |
| `SOLIDPING_EMAIL` | - | Email for non-interactive `auth login` |
| `SOLIDPING_PASSWORD` | - | Password for non-interactive `auth login` |

This makes the client convenient in CI: set `SOLIDPING_URL`, `SOLIDPING_EMAIL`, and `SOLIDPING_PASSWORD` (or use a token) and run `sp` commands directly.
