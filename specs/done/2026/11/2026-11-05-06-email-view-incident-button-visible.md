---
model: haiku
effort: low
pr: batch
automerge: false
---

# The "View incident" button in emails is a white outlined button that barely reads as clickable

## Problem
In the incident-resolved style emails (e.g. "Home est de nouveau stable"), the only call to action, "Voir l'incident", uses the `secondary` button variant: white background, 1px grey border, dark text (`base.html:106-107`). On a white card it looks like a text field, not a button. Dark mode is the same (`base.html:151-152`).

## Proposal
1. Standalone CTAs (the only button in the email) switch from `"Variant" "secondary"` to `"Variant" "primary"` (blue `#1e64ef`, white text, already defined at `base.html:102-103`) in:
   - `incident-resolved.html:23`
   - `incident-degraded-resolved.html:18`
   - `incident-burn-resolved.html:27`
   - `incident-acknowledged.html:15`
   - `incident-comment.html:14`
2. Leave `base.html:300` as is: there "View incident" sits next to the primary "Acknowledge" button, so it must stay secondary to keep the hierarchy.
3. Do not change the `secondary` CSS itself, other emails rely on it next to a primary button.

## Tests
- `internal/email/textpartials_test.go` (or the template render test next to it): render `incident-resolved` and assert the HTML contains `btn-primary` and not `btn-secondary` around the view-incident link.
- Negative case: render the alert email with both actions (`base.html:299-300`) and assert it still has one `btn-primary` (ack) and one `btn-secondary` (view incident).

## To verify
- Whether any other template uses `secondary` as the sole button (`grep -n '"secondary"' internal/email/templates/*.html`); apply the same change if so.
