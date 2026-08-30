---
sidebar_position: 21
title: TV Mode (Wallboards)
---

# TV Mode

A very common home for a status page is a television on an office wall: a
screen nobody touches, that anyone glancing up can read from across the room.
The ordinary [status page](status-pages.md) is not that. It is a document
written for a reader at 40 cm — sections, per-resource rows, response-time
charts, a scrollbar — and from four metres away none of it is legible.

**TV mode** is the same status page rendered for that distance. Add `/tv` to
any status page URL:

```
https://status.acme.com/tv                       # a custom domain
https://solidping.example.com/status0/acme/tv    # the org's default page
https://solidping.example.com/status0/acme/main/tv
```

There is nothing to configure. TV mode is a display mode of a page you already
built: the same curation, the same branding, the same availability thresholds,
the same data. It is not a second place to decide which checks matter — the
status page already is that decision.

The dashboard gives you the URL directly: open the status page under
**Status Pages**, and the **TV mode** card carries a copy button.

## What it shows

A single non-scrolling viewport, top to bottom:

- **The ambient state.** The whole background is tinted by the page's rollup:
  green operational, orange degraded, red down, blue for a planned maintenance
  window, grey when nothing is known. The tint is always paired with an icon
  and the state written out in words — roughly one man in twelve cannot
  separate this green from this red, so colour is never the only signal.
- **The uptime number**, big, over the page's own history window ("30-day
  uptime"). Shown only when the page publishes availability at all.
- **Days since the last incident**, from the page's public incident history.
  Hidden while an incident is open, because "42 days since the last incident"
  next to a live outage is a contradiction on a wall.
- **Active incidents**: title, severity, affected components, and a live
  duration ("ongoing for 1h 12m"). More than two active at once are cycled
  every ten seconds rather than shrunk — three unreadable cards convey less
  than one readable one.
- **The last three resolved incidents**, with how long each one took.
- **A footer** with the page's name, a live pulse, and the time of the last
  successful refresh.

Nothing on the board is interactive, and the mouse cursor hides itself after a
few seconds of stillness.

### A maintenance window is blue, never red

A scheduled window paints the board blue, not red, even though checks are
failing inside it. A wall of red at 03:00 during a planned migration is how a
room learns to ignore the screen.

### A published incident always shows

An incident you published by hand escalates the board even while every probe
is green — `critical` to red, `major` and `minor` to at least orange. You
published it because you know something the checks do not, and a board that
kept insisting "all systems operational" over an open critical incident would
be worse than no board.

## When the data stops arriving

The board refreshes every 30 seconds, and every 15 seconds while anything is
wrong. After 90 seconds without a successful refresh — three missed polls —
the **entire board turns grey** and says "no update received since HH:MM".

This matters more than it looks. A frozen green screen during an outage is
worse than a dark one: the room reads it as reassurance. The board recovers on
its own the moment a refresh succeeds.

## The uptime number, precisely

The big percentage is the **arithmetic mean of the per-resource uptime
percentages** shown on the ordinary page, over that page's configured history
period (24 h, 7 d, 30 d or 90 d).

Two details follow from that definition:

- **Resources with no data are excluded**, not counted as 0 % or 100 %. A
  check that has never reported is not an outage, and it is not perfect uptime
  either. If nothing on the page has data, no number is shown at all.
- **It is the mean, not a time-weighted union** of every probe. A union
  ("any resource down means the page is down") is stricter and arguably closer
  to how a customer experiences an outage, but it cannot be reconciled with the
  rows on the page without explaining probe weighting. The mean is a number a
  reader can verify: every component's percentage is listed on the page, and
  their average is arithmetic anyone can do in their head.

The same number is available to scripts on the page payload and its summary,
as `overallAvailabilityPct`.

## Non-public pages: kiosk tokens

A public page needs nothing — its TV URL just works.

A [password-protected or private page](status-pages.md#private-and-password-protected-pages)
cannot be opened by an unattended screen. The password unlock lasts 12 hours,
so somebody would be re-typing it on the television every morning; a private
page answers 404 on every public URL by design.

A **kiosk token** is the answer: one long-lived, revocable secret per page that
grants **read-only view of that one page**. It bypasses the password prompt and
turns a private page into an unlisted one — for that screen only.

Generate one from the **TV mode** card on the status page, then use the URL it
gives you:

```
https://status.acme.com/tv?kiosk=<token>
```

The screen reads the token once, keeps it in memory, and removes it from the
address bar, so the secret is not left legible on a wall for months.

### What a kiosk token does and does not do

| It can | It cannot |
|---|---|
| Render the TV board for its own page | Open any other page |
| Read that page's public JSON, summary and incident history | Change anything |
| Work indefinitely, unattended | Reach the dashboard or the API |

Other properties worth knowing:

- **The token is shown once**, at creation. Only a hash of it is stored, so
  there is nothing to reveal later — if you lose it, regenerate.
- **Regenerating replaces it.** The screen still using the old URL stops
  working immediately. That is the revocation mechanism, and it is why the
  dashboard asks before doing it.
- **An invalid or revoked token behaves exactly like no token at all** — a
  password page still answers 401, a private page still answers 404. There is
  no "bad token" reply, because such a reply would confirm that a private page
  exists, which is precisely what `private` is bought to prevent.
- **Disabling the page beats the token.** A disabled page is 404 for everyone,
  wallboards included: switching a page off is how you take a screen down.
- **Kiosk-authenticated responses are `private, no-store`**, like
  password-unlocked ones, so no shared cache retains them.
- The token unlocks TV mode and the JSON the board reads. It deliberately does
  not unlock the ordinary page route — `/{org}/{page}?kiosk=…` still shows the
  password prompt.

## A wallboard nobody maintains

TV mode reuses the page's curation, which means it inherits its blind spots
too: a check nobody attached is a service the board silently claims is fine.

The combination that removes that is a **private** page, a section set to **all
checks** ([dynamic sections](status-pages.md#dynamic-sections)), and a kiosk
token on the television. Every check the team creates appears on the screen from
the moment it exists, with nothing to remember and nothing to configure per
service.

"All checks" is the right rule here precisely *because* the page is private —
there is no audience to disclose anything to. On a **public** page the same rule
publishes every future check to the internet, which is why the dashboard warns
about it there and why a `public=true` label opt-in is the recommendation
instead.

## Notes and limits

- **An org page slugged `tv` collides with the default-page URL.** `/acme/tv`
  is TV mode for `acme`'s default page; a page whose slug is literally `tv` is
  still reachable at `/acme/tv/tv`.
- Response-time charts are deliberately absent from the board. From four
  metres a chart is decoration; the incident title, its duration and the
  affected components carry the information.
- The board commits to a dark, tinted background regardless of the viewer's
  theme. A screen that runs 24/7 in an office should not be a lamp.
