---
model: opus
effort: medium
---

# WhatsApp alert: add a button linking to the check

## Problem

The `whatsapp` escalation alert delivers four body variables (check name, state,
detail, org) and nothing else. A recipient woken at 3am reads "Payments API is
now DOWN" and then has to leave WhatsApp, find the dashboard, log in and search
for the check by hand. Every other paging channel does better: the SMS body
carries a signed ack magic-link
([job_escalation_step.go:852](server/internal/jobs/jobtypes/job_escalation_step.go#L852)),
and email/Slack notifications embed links.

WhatsApp is the one channel where the alert is a dead end.

This was confirmed end to end on 2026-08-10: a real alert was delivered through
the production WABA (`status=sent`, real `wamid` recorded) and contains no link
of any kind.

## Proposal

Add a **"Visit website" URL button** to the `solidping_alert` template pointing
at the check's dashboard page, and populate it from `pageWhatsApp`.

Target chosen: **the check page**, `/dash0/orgs/{org}/checks/{checkUid}` (route
`checks.$checkUid.tsx`). Not the ack link — acknowledging is a separate action
with its own semantics, and this button is about *seeing what broke*. An ack
button can be added later as a second button if wanted (Meta allows several).

### The client already supports this

`whatsapp.TemplateMessage.ButtonParam` already emits exactly the component a
dynamic URL button needs
([client.go:339](server/internal/integrations/whatsapp/client.go#L339)):

```json
{ "type": "button", "sub_type": "url", "index": "0",
  "parameters": [{ "type": "text", "text": "<suffix>" }] }
```

It was written for the authentication copy-code button, and its doc comment says
so. **No payload change is needed** — only the comment must be generalised, since
the same field now serves two purposes. Worth an explicit note in the code so the
next reader does not assume it is authentication-only.

### The design decision: where the base URL lives

A WhatsApp URL button is **not** a free-form URL. The template fixes a static
prefix at creation time and accepts one variable suffix:

```
https://solidping.io/dash0/  +  {{1}}
```

The base is therefore baked into the *approved template*, not into config. That
has a consequence the spec must state plainly: **the template is not portable
across installations**. A self-hoster on `status.acme.com` cannot use a template
whose button points at `solidping.io`.

Options considered:

1. **Each installation creates its own template with its own base.** The button
   prefix is part of the documented template definition, alongside the body.
   Chosen: it is honest about the constraint, needs no code, and matches the
   fact that templates are already a per-installation manual prerequisite.
2. Pass the whole URL as the suffix with a dummy prefix (`https://` + `{{1}}`).
   Meta rejects prefixes that are not a valid concrete URL, and it would make
   every alert button look like a phishing redirect. Rejected.
3. Ship a solidping.io redirector that forwards to the installation. Adds a
   hosted dependency and a privacy leak (every self-hoster's incident traffic
   transits our domain). Rejected.

So: `whatsapp.alert_button_base` is **not** a new config key. The base lives in
the template; SolidPing only supplies the suffix.

### Suffix construction

`pageWhatsApp` sets `ButtonParam` to the path *after* the documented prefix:

```
orgs/{orgSlug}/checks/{checkUID}
```

with the documented prefix being `{baseURL}/dash0/`. `orgSlugFor` already
resolves the slug; `baseURL` comes from `appBaseURL(jctx)` as the SMS path does.

Constraints to respect, all of which cause a silent send failure otherwise:

- The suffix must be **URL-safe** — slug and UID are already, but escape
  defensively rather than trusting it.
- Meta caps the button parameter length; keep it bounded like
  `whatsAppDetailCap` does for the detail line.
- If the org slug cannot be resolved (`orgSlugFor` returns ""), **send without
  the button rather than with a broken link**. A wrong link is worse than none.

### Template change is not free

`solidping_alert` is already APPROVED on the production WABA
(`1561851968762906`). Adding a button **requires an edit and a new review**, and
an edited template can be rejected. Plan for the alert channel to fall back to
the current bodies-only template while the edited one is in review — i.e. do not
make the button mandatory in the send path: an installation whose template has
no button must keep working, which falls out naturally since `ButtonParam` is
only added when non-empty.

## Tests

- Client: payload includes the button component when `ButtonParam` is set, and
  omits it entirely when empty (already partly covered — extend rather than
  duplicate).
- `pageWhatsApp`: builds the expected suffix from org slug + check UID; falls
  back to no button when the slug is unresolvable; caps an overlong suffix.
- Escalation: a send with the button still records `status=sent` and the `wamid`
  in the audit row.
- Docs: the template definition in `web/docs/docs/configuration/whatsapp.md`
  gains the button prefix, with the portability constraint stated.

## Out of scope

- An ack button (second button) — separate decision, separate approval.
- Buttons on the verification template; Meta fixes its shape.
- Localised button labels.
