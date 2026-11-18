---
sidebar_position: 6
title: Record a flow with Chrome and import it
description: Record a login or checkout flow in Chrome DevTools, export it as JSON and import it as a SolidPing JavaScript browser check.
---

# Record a flow with Chrome and import it

You do not need to write a browser check by hand. Chrome ships a recorder that
replays flows locally, and SolidPing turns its JSON export into a
[JavaScript browser check](../javascript-checks.md).

## Record

1. Open the page to monitor in Chrome, then **DevTools > Recorder** (open the
   DevTools with `F12`, then the `⋮` menu > **More tools > Recorder**).
2. **Create a new recording**, name it (the name becomes the check's name) and
   click through your flow: sign in, search, add to cart.
3. **End recording**, then replay it once in Chrome to make sure it is stable.
4. Click **Export** and choose **JSON**.

## Import

Open **Checks > More actions > Import**, pick **Chrome Recorder (exported
JSON)**, paste or upload the file, review the dry-run preview and confirm. You
can also call the API: `POST /api/v1/orgs/:org/checks/import/convert?source=chrome-recorder`
with the JSON as body (add `dryRun=true` to preview).

The flow must start with a `navigate` step (Chrome records one when you start
from a blank tab or enter a URL).

## What is converted

| Recorder step | Generated script |
|---|---|
| `navigate` | `page.goto(url)` |
| `click`, `doubleClick` | `page.click([...selectors])`, the recorded selectors are tried in order |
| `change` | `page.fill(selectors, value)` |
| `keyDown` + `keyUp` | `page.press(last field, key)` |
| `waitForElement` | `page.waitFor`, plus count, visibility, attribute and property assertions |
| `waitForExpression` | polls the expression until it is truthy |
| `assertedEvents` navigation | after the step, the page URL must match |
| `hover`, `scroll`, `setViewport`, `emulateNetworkConditions`, `close` | a comment (no effect on a check) |

Every step is wrapped in a named `step()`, so a failing run says which step
failed.

## Passwords

A `change` step has its value moved to the check's **encrypted secrets**
(`RECORDER_SECRET_1`, ...), and the script reads `secrets.RECORDER_SECRET_1`,
when either is true:

- one of its selectors looks like a credential field: it matches `password`,
  `pwd`, `pass`, `pin`, `otp`, `secret`, `token`, `api key`, `mot de passe`,
  `passwort` or `contraseña`, or carries `type=password`;
- the value looks like a credential: a `Bearer ` token, or a long random string
  with no spaces.

Other typed values (emails, search terms) stay in clear in the script, and the
import warns with their count ("N typed value(s) stayed in the script"): move any
credential among them to `secrets.NAME` before confirming.

## What is not converted

Steps that cannot be translated leave a `// TODO` line and a warning, and the
check is created **paused**: steps inside an iframe, steps on another tab or
popup, custom steps and unknown step types. Selectors that cross a shadow root
or frame are skipped, and so are non-primary mouse buttons and lone key events
(modifiers).
