---
model: opus
effort: medium
---

# Emails still carry the old blue-to-crimson palette: re-tune base.html to the electric identity

Spec 5 of the "electric identity" series. The palette (hex column) comes from
[spec 01](2026-09-24-01-dash0-electric-identity-tokens-and-primitives.md)
§1 and §2, and the navy from
[spec 02](2026-09-24-02-dash0-electric-identity-app-chrome.md) §1. It has no
code dependency on either, so it can land in any order.

## Problem

All 27 templates in `server/internal/email/templates/` share
[base.html](server/internal/email/templates/base.html). Its `<style>` block
is inlined by go-premailer (`formatter.go:330`), and it says it mirrors
dash0's `--primary #0072D5` and `--brand #DA1D69`. Once spec 01 lands, the
mail a user receives no longer matches the app it links to:

- **Header** (l.60): navy `#16273a → #0f1a24 → #0a121a`. It is close to the
  new sidebar navy, but a different hue.
- **Accent bar** (l.74): `#0072d5 → #2b9bf4 → #da1d69`. It fades into
  crimson, which is the old identity.
- **Links** (l.89), section title rule, quote rule, `.btn-primary`
  (`#2b8ee6 → #0072d5`) and the legacy `.cta` all use `#0072d5`.
- **The designed dark palette** (the `prefers-color-scheme` block, about
  l.181-220) has its own blue values.

## Proposal

Change colors only, in `base.html`. No layout, markup, copy or template
logic changes.

| Element | Today | New |
|---|---|---|
| `.header` fallback / gradient | `#0f1a24` / `#16273a → #0f1a24 → #0a121a` | `#07112a` / `linear-gradient(135deg, #0a1731 0%, #07112a 55%, #04091b 100%)` |
| `.accent-bar` fallback / gradient | `#0072d5` / `#0072d5 → #2b9bf4 → #da1d69` | `#175ee8` / `linear-gradient(90deg, #00b0e4 0%, #175ee8 50%, #453cdb 100%)` |
| Links, `h2.section-title` rule, `.quote` rule | `#0072d5` | `#1e64ef` |
| `.btn-primary td` fallback / gradient | `#0072d5` / `#2b8ee6 → #0072d5` (180deg) | `#1e64ef` / `linear-gradient(135deg, #007bce 0%, #175ee8 50%, #453cdb 100%)` |
| `.btn-primary` shadow | `rgba(0,114,213,0.45)` | `rgba(30,100,239,0.45)` |
| `.cta` (legacy) | `#0072d5` | `#1e64ef` |
| `.header-wordmark` | `#94a3b8` | `#8b9ab3`, a blue-grey that reads on the new navy (check ≥ 4.5:1) |
| Neutrals: content text `#1e293b`, headings `#0f172a`, eyebrow `#64748b`, page `#f1f5f9`, card borders | slate | Move them toward the new neutrals where a direct equivalent exists: `#09121f` (fg), `#586474` (muted fg), `#dee3eb` (border), `#f5f9fc` (page). Do not retune every grey. Only the ones that visibly clash next to the new blue. |
| Dark palette links and buttons | old blues | `#57a8ff` for links (dark `--primary`). The button keeps the same gradient with white text. |

Rules:

- **Every gradient keeps a solid `background-color` fallback** that is
  readable with white text (`#1e64ef`, 5:1). The comment at l.56-59 explains
  why: Outlook ignores `background-image`.
- **Status banners and `.btn-success` are unchanged**: `.status-down`,
  `-recovered`, `-escalated`, `-reopened`, `-comment`, `-acknowledged`. They
  carry the meaning of an alert.
- The **light-only pin stays**. It is documented at the top of `base.html`
  and in [wiki/features/email-dark-mode.md](wiki/features/email-dark-mode.md).
- **The logo stays crimson** (`productLogoURL` → `/dash0/logo.png`). A
  crimson mark on the navy header is the intended look.
- Update the "mirrors dash0" comment to the new values
  (`--primary #1e64ef`) and drop the `--brand #DA1D69` reference.
- `uptime-report.html`, `uptimereport/report.go` and `metrics.go` keep their
  status ramps.

## Out of scope

- Template structure, copy, and the org or status-page logo logic.
- Extending `TestFormatter_AllShippedTemplatesRenderCleanly` to all 27
  templates (it covers 12 today). It is worth doing, but it is its own change.
- Slack, Discord, Mattermost and Teams embed colors, and the SVG badges.

## Acceptance criteria

- [ ] `base.html` has no `#0072d5`, `#2b8ee6`, `#2b9bf4` or `#da1d69` left
      (`grep -i` returns nothing).
- [ ] Every gradient in `base.html` still has a solid `background-color`
      fallback.
- [ ] White text on `.btn-primary`'s fallback, and on each gradient stop, is
      ≥ 4.4:1.
- [ ] Status banners and the success button are byte-identical to before.
- [ ] `TestFormatter_AllShippedTemplatesRenderCleanly` and the rest of
      `server/internal/email` tests pass.

## Verification

- `go test ./server/internal/email/...` and `make lint-back`.
- Walk every template in the email preview (the dash0 email preview route,
  light and `?colorScheme=dark`), at least: incident down/recovered,
  password reset, invite, weekly uptime report. Screenshots in the PR.
