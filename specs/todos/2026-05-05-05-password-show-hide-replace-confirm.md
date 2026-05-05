# Replace confirm-password with a show/hide toggle

## Context

Three forms in dash0 currently ask the user to type their password twice:

- `web/dash0/src/routes/invite.$token.tsx` — fields `password` + `confirmPassword`
- `web/dash0/src/routes/reset-password.$token.tsx` — same pair (lines 24–25)
- The registration flow (verify exact route during impl — likely under the
  login route or a dedicated register page reachable from `/orgs/$org/login`)

Each enforces equality client-side and rejects on mismatch. The pattern is a
holdover from the era when password fields were always masked and you had no
way to see what you'd typed. Two arguments to retire it:

1. **Users routinely paste the same value into both fields**, defeating the
   typo check. Modern password managers do this automatically.
2. **A show/hide eye toggle gives the user direct evidence** of what they
   typed, which is a stronger guard against typos than typing the same
   error twice.

Industry has moved: Stripe, Notion, Linear, Vercel, Slack — none ask for
confirm-password on signup. The pattern survives in older enterprise UIs and
auditor checklists, both worth deferring to user experience here.

## Goal

Every password input in dash0 has an eye-icon toggle next to it. No form asks
the user to type their password twice. Validation (min 8 chars,
backend-side strength) is unchanged.

## Scope

In scope:

1. **New shared component** `web/dash0/src/components/ui/password-input.tsx`:
   - Wraps `<Input type="password" />` with a right-aligned eye / eye-off
     icon button (Lucide: `Eye`, `EyeOff`).
   - Toggling switches the input between `type="password"` and `type="text"`.
   - Accepts the same props as `<Input>` plus an optional `showLabel`
     prop for screen readers (default `"Show password"` / `"Hide password"`,
     i18n via the existing namespace).
   - Accessibility: the toggle is a real `<button>` with `aria-pressed` and
     `aria-label`; tap-target ≥ 44 px on touch devices; keyboard reachable
     via tab.
   - Autofill: the input keeps `autocomplete="new-password"` (or
     `current-password` for login), so 1Password / Chrome / iCloud Keychain
     still recognize it.

2. **Replace password + confirmPassword pairs** in:
   - `invite.$token.tsx` — drop `confirmPassword` state, the validator
     branch, and the second input. Use `<PasswordInput>` for the remaining
     field.
   - `reset-password.$token.tsx` — same.
   - The signup/register form (locate during impl, likely under
     `routes/orgs/$org/login` or a sibling).
   - Settings → change password, if applicable. Verify during impl; current
     password + new password is fine, *new* password + confirm-new is what
     gets removed.

3. **Keep validation**:
   - Min 8 chars client-side (already there).
   - Server-side rules unchanged.
   - Show inline character-count or strength hint? **Out of scope** — keep
     this PR focused. A strength meter is its own decision.

4. **i18n** (`web/dash0/src/locales/{en,fr,de,es}/`):
   - Add `auth:showPassword` / `auth:hidePassword`.
   - Remove `auth:confirmPassword` and `auth:passwordsDoNotMatch` (and any
     siblings in fr/de/es).
   - All four locales land in the same PR.

5. **Tests**:
   - e2e: existing tests that fill `confirmPassword` need the step deleted,
     not adapted.
   - Component test for `<PasswordInput>`: toggle flips type, aria-pressed
     reflects state, autocomplete prop is passed through.

Out of scope:

- Password strength meter / live entropy hint.
- Backend password policy changes.
- Replacing password authentication entirely (passkeys, magic links — see
  security review in the conversation log).
- Any forms outside dash0 (status0 has no password forms; dash is being
  phased out per `web/CLAUDE.md`).

## Edge cases

- **Mobile show/hide**: must work cleanly on touch — no hover-only states.
  Eye icon button stays visible; tapping it flips the type.
- **Password manager interference**: when 1Password fills the input, the
  toggle should still flip the masked → plaintext display. Test in actual
  1Password / Chrome / Safari before merging.
- **Submitting while plaintext is visible**: form submission proceeds
  normally; the field's `value` is the same regardless of `type`. No special
  handling.
- **Auto-mask after blur?** Common UX is to re-mask the input when it loses
  focus, to prevent shoulder-surfing on a screen the user has walked away
  from. Recommendation: yes, re-mask on blur. Trivial to implement; aligns
  with browser password-manager autofill which also re-masks.
- **Caps-lock warning**: orthogonal to this spec. Don't add it here.

## Test plan

- [ ] Manual: invite-acceptance, password-reset, register flows. Type
      password, click eye, verify it shows. Type a new char in plaintext,
      verify it's added. Click eye again, verify masked.
- [ ] Manual: tab through the form. Focus order: password input → eye toggle
      → submit. Eye toggle is reachable and pressable with Enter/Space.
- [ ] Manual: 1Password / Chrome / Safari Keychain autofill still
      identifies the input as a password field and offers to save / fill.
- [ ] Manual: mobile (iOS Safari, Chrome Android). Eye button is at least
      44 px tappable. No layout overflow on narrow screens.
- [ ] e2e: existing tests that fill `confirmPassword` are removed; new flow
      passes.
- [ ] Lint: no unused i18n keys, no unused state.

## Files touched (estimate)

- `web/dash0/src/components/ui/password-input.tsx` (new)
- `web/dash0/src/routes/invite.$token.tsx`
- `web/dash0/src/routes/reset-password.$token.tsx`
- `web/dash0/src/routes/<register>` (locate)
- `web/dash0/src/routes/orgs/$org/settings*` (verify change-password form)
- `web/dash0/src/locales/{en,fr,de,es}/auth.json`
- `web/dash0/e2e/<auth-related-specs>.ts`

## Coordination with sibling specs

- `2026-05-05-02` (drop email field on invite) and `2026-05-05-04` (auth
  flows audit) both touch the same files. Order of merge:
  1. `02` ships first (smallest, isolated to invite page).
  2. `04` ships next (audit sweep, larger surface but no deep changes).
  3. `05` ships last and rebases on top — replacing the password pairs
     across the now-cleaner forms.

  If they ship out of order, the conflicts are mechanical, not semantic.
  Resolve by re-applying this spec's changes to whatever the form looks like
  at merge time.
