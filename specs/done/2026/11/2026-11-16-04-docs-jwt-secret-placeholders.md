---
model: opus
effort: low
---

# The JWT secret examples published in the docs are accepted as real signing keys

## Problem

Found by the security scan of 2026-11-16 (finding AO4). Severity: Low. It takes an operator who copies a documentation example verbatim. Anyone who reads the public docs then knows the signing key and can forge a session for any account. This is the variant of #895 (`config.IsPlaceholderJWTSecret`) that #895 did not cover.

- #895 refuses `""`, `change-me-in-production` and the dev compose's `dev-secret-change-in-production` (`server/internal/config/discord_oauth.go:57-64`, used by `ensureJWTSecret` in `server/internal/systemconfig/systemconfig.go:1608-1625`).
- The configuration docs publish two other literal values as copy-paste examples:
  - `SP_AUTH_JWT_SECRET=your-secure-random-secret` (`web/docs/docs/configuration/index.md:400`);
  - `jwt_secret: your-secure-random-secret` (`index.md:427` and `web/docs/docs/configuration/authentication.md:546`);
  - `SP_AUTH_JWT_SECRET=your-secure-random-secret-at-least-32-chars` (`authentication.md:51`).
- Any non-placeholder env or config value short-circuits generation, so these strings become the HS256 key.
- No minimum length or entropy check applies either.

## Proposal

1. Change the doc examples so that they are not valid values: a command such as `SP_AUTH_JWT_SECRET=$(openssl rand -base64 48)`, or an obviously non-literal marker. Say that leaving the variable unset auto-generates a secret.
2. Add the two published strings to `IsPlaceholderJWTSecret`, so copies already in production are replaced at the next boot. The existing warning is logged. Note that replacing the key ends every current session, which is the point.
3. At startup, log a warning when a non-placeholder secret is shorter than 32 bytes. Do not refuse it: an operator may rely on a short one, and refusing would lock them out.

## Tests

- `server/internal/config` test: `IsPlaceholderJWTSecret` is true for both published strings. This fails before the fix. Positive control: a random 48-byte value is false.
- `server/internal/systemconfig` test, next to the existing `DevJWTSecret` case (`systemconfig_cover_test.go:204`): with `SP_AUTH_JWT_SECRET=your-secure-random-secret`, `ensureJWTSecret` generates and stores a fresh secret instead of using the env value.
- A docs lint or unit test greps `web/docs/docs` for `jwt_secret:` / `SP_AUTH_JWT_SECRET=` followed by a literal on the placeholder list, so a new example cannot reintroduce one.
