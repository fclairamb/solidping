# Auth-page headline translations are literal calques that read unnatural

## Problem

The dashboard's logged-out marketing panel (`web/dash0/src/components/layout/auth-split-layout.tsx:38`,
key `marketing.headline`) uses the English tagline:

> "Uptime monitoring that <accent>never blinks.</accent>"

The wordplay works in English because "blink" covers both the vigilant eye
(never sleeps, never looks away) and a status light (never flickers, never
goes dark). The current FR/ES/DE translations are word-for-word calques that
lose the idiom and read oddly to native speakers:

- `web/dash0/src/locales/fr/auth.json:3` — "Une surveillance qui <accent>ne cligne jamais.</accent>"
  "cligner" without "des yeux" is not idiomatic, and a monitoring service that
  "cligne" doesn't evoke vigilance in French. The "uptime" qualifier was also dropped.
- `web/dash0/src/locales/es/auth.json:3` — "Monitorización que <accent>nunca parpadea.</accent>"
  "parpadear" does apply to lights, but as a slogan it reads flat/ambiguous
  (a screen that "parpadea" is glitching, not vigilant).
- `web/dash0/src/locales/de/auth.json:3` — "Verfügbarkeitsüberwachung, die <accent>niemals blinzelt.</accent>"
  "blinzeln" is strictly the human eye-blink; a product that "niemals blinzelt"
  sounds comical rather than reassuring.

## Proposal

Replace the literal calques with idiomatic equivalents that keep the intent
("always watching, never misses anything") rather than the literal image.
Candidate directions to review with native speakers:

- **FR**: "Une surveillance qui <accent>ne ferme jamais l'œil.</accent>"
  (real idiom: never sleeps a wink, keeps the eye metaphor) — or the simpler
  "Une surveillance qui <accent>ne dort jamais.</accent>"
- **ES**: "Monitorización que <accent>no quita ojo.</accent>"
  (idiom "no quitar ojo" = keeps close watch) — or
  "Monitorización que <accent>nunca duerme.</accent>"
- **DE**: "Verfügbarkeitsüberwachung, die <accent>nie ein Auge zutut.</accent>"
  (idiom "kein Auge zutun" = not sleep a wink) — or
  "Verfügbarkeitsüberwachung, die <accent>niemals schläft.</accent>"
  ⚠️ Avoid "ein Auge zudrücken", which means *turning a blind eye* — the
  opposite of the intended message.

Constraints:

- Keep the `<accent>…</accent>` markup wrapping the punch clause (used by the
  `Trans` component in `auth-split-layout.tsx`), including the trailing period
  inside the accent as today.
- Keep length in the same ballpark so the split-layout panel doesn't wrap
  awkwardly on mobile — verify all four locales visually after the change.
- Only `web/dash0/src/locales/*/auth.json` is affected; the marketing site
  (`solidping-website` repo) has its own copy and is out of scope here.

Open question: final wording should get a native-speaker sanity check (or at
least a deliberate pick between the "eye" idiom and the plainer "never sleeps"
variant per language — consistency across the three languages is not required
if one variant lands better in a given language).

## Implementation Plan

Scope is copy-only: three JSON string values, one per non-English locale. The
English source (`en/auth.json`) is idiomatic already and stays untouched.

1. **Pick the wording.** Go with the "eye" idiom variant in every language so
   the metaphor stays coherent with the English "blink" and length stays in the
   same ballpark:
   - FR `web/dash0/src/locales/fr/auth.json` → `"Une surveillance qui <accent>ne ferme jamais l'œil.</accent>"`
     (real idiom "ne pas fermer l'œil" = never sleep a wink; keeps the eye image).
   - ES `web/dash0/src/locales/es/auth.json` → `"Monitorización que <accent>no quita ojo.</accent>"`
     (idiom "no quitar ojo" = keeps a close watch).
   - DE `web/dash0/src/locales/de/auth.json` → `"Verfügbarkeitsüberwachung, die <accent>nie ein Auge zutut.</accent>"`
     (idiom "kein Auge zutun" = not sleep a wink; deliberately NOT "ein Auge
     zudrücken", which means turning a blind eye).
2. **Edit the three `headline` values**, preserving the `<accent>…</accent>`
   markup and the trailing period inside the accent span exactly as today. No
   other key in the files changes.
3. **QA:** `make build-dash0` then `cd web/dash0 && bun run lint` (no new
   errors); confirm the four locales stay a comparable length for the
   split-layout panel. Backend is untouched, so no Go targets needed.
