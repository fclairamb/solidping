# Security Policy

## Supported Versions

SolidPing is pre-1.0 and ships continuously from `main` via release-please.
Only the latest minor release is supported with security fixes.

| Version | Supported |
| --- | --- |
| latest | :white_check_mark: |
| anything older | :x: |

## Reporting a Vulnerability

**Please do not open a public GitHub issue for a security vulnerability.**

The preferred way to report a vulnerability is GitHub's private advisory
form:

**https://github.com/fclairamb/solidping/security/advisories/new**

This lets us discuss and fix the issue before it's public. If you'd rather
not use GitHub, you can instead email:

**security@solidping.io**

### What to expect

This is a single-maintainer project, so reports are acknowledged and
triaged as quickly as the maintainer can manage — we don't commit to a fixed
response time or SLA. Once a fix is ready, we coordinate disclosure with the
reporter and are happy to credit you in the release notes if you'd like.

### Scope

- The SolidPing codebase is shared between the hosted SaaS
  (`solidping.io`) and self-hosted deployments — a report against either
  applies to both.
- Private agents (`sp agent`) and the browser-check CDP sidecar are in
  scope.
- Third-party services SolidPing integrates with (Slack, Telegram, OVH,
  Cloudflare, and so on) are not in scope — please report issues in those
  services to their own maintainers.
