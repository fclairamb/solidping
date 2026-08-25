# Evaluate registering SolidPing on Pilot Protocol

> **Reminder / spike, not a commitment.** Requested 2026-07-30: check whether
> SolidPing can (and should) be published as a service agent on
> [Pilot Protocol](https://pilotprotocol.network/docs/getting-started).

## What Pilot Protocol is

An open-source **overlay network for AI agents**: encrypted peer-to-peer
transport with no API keys, accounts, or VPN setup. Each agent gets a permanent
virtual address; peers connect through mutual-trust handshakes. Agents can
discover and query "specialist service agents" — which is the slot SolidPing
would occupy (an agent that answers uptime/health questions about monitored
endpoints).

Onboarding per the getting-started page (as of 2026-07-30):

1. Install: `curl -fsSL https://pilotprotocol.network/install.sh | sh`
2. Start the daemon: `pilotctl daemon start --email <email> --hostname my-agent`
3. Handshake with peers: `pilotctl handshake <agent-name>`
4. Exchange messages once handshakes are approved

There is **no central registration** — identity is a locally stored keypair, so
"inscription" means running a daemon and being discoverable, not filling a form.

## Questions to answer

- Is there actually a directory/registry of service agents, or is discovery
  purely handshake-by-name? Without a registry there is little distribution
  value for SolidPing.
- What would the SolidPing agent expose? Candidates: check status lookup,
  incident summary for an org, "is $host up". All of it needs auth scoping —
  Pilot's transport trust is not our org/permission model.
- How does authentication map? Pilot handshakes authenticate a *peer*, not a
  SolidPing user/org. Any bridge needs to bind a Pilot identity to a SolidPing
  API token, or be restricted to public status-page data only.
- Deployment shape: a sidecar daemon next to the server, or a separate small
  bridge process talking to the public REST API? The latter keeps the daemon out
  of our container and our supply chain.
- Traction check: how many real agents are on the network, is the project
  maintained, what is the license and governance?

## Cautions

- The install path is `curl | sh`, and the installer **injects capabilities into
  agent frameworks** (Claude Code, OpenHands) on the machine it runs on. Do not
  run it on a dev machine or in CI without reading the script first; evaluate in
  a throwaway VM or container.
- Running an overlay-network daemon inside the SolidPing production container
  would add an unaudited network listener next to the monitoring workers. If we
  ship anything, prefer an out-of-tree bridge in its own repo/deployment.

## Suggested outcome

A short verdict written back into this spec: **worth it / not worth it**, plus
either a follow-up spec for a bridge or a note in `wiki/` explaining why we
passed. No production change should land straight out of this spike.
