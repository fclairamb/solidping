---
sidebar_position: 5
title: CLI Client
---

# CLI Client

SolidPing ships a command-line client, `sp`, for managing your monitoring from the terminal or from scripts and CI pipelines. It talks to the same REST API as the dashboard, so anything you can do in the UI you can also automate.

## Authentication

Log in once and the client stores your session:

```bash
sp auth login          # prompts for org, email, password
sp auth me             # show the current user
sp auth switch-org      # change active organization
sp auth logout
```

## Common Commands

| Command | Description |
|---------|-------------|
| `sp checks list` | List checks |
| `sp checks get <uid>` | Show a check |
| `sp checks add` / `update` / `upsert` | Create or modify checks |
| `sp checks deps` | Manage check dependencies |
| `sp checks remove <uid>` | Delete a check |
| `sp results list` | List check results (with filters) |
| `sp incidents list` / `get` | Inspect incidents and their events |
| `sp events list` | Browse the audit event log |
| `sp tokens list` / `create` / `revoke` | Manage API tokens |
| `sp members list` / `add` / `update` / `remove` | Manage organization members |
| `sp jobs list` / `get` / `create` / `cancel` | Manage background jobs |
| `sp system get` / `set` / `delete` | Read and write system parameters |
| `sp server health` / `version` | Check server status |

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
