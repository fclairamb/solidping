# Custom User-Agent

## Goal
Allow customizing the User-Agent string used by the server when performing checks, via the `SP_USERAGENT` environment variable.

## Details
- Default agent name: `solidping.io`
- Override via: `SP_USERAGENT=<custom-name>`
- The agent name must be used in every protocol that declares an identity:
  - **HTTP/HTTPS**: `User-Agent` header
  - **SMTP**: `EHLO` hostname
  - **Any future protocol** that has an agent/client identity field
- The configured name is visible in the server configuration / logs at startup

## Acceptance Criteria
- [ ] All HTTP checks use `solidping.io` as User-Agent by default
- [ ] SMTP checks use `solidping.io` as EHLO hostname by default
- [ ] Setting `SP_USERAGENT=mycompany.com` changes the identity across all protocols
- [ ] Any new protocol check that supports agent identification must use this setting

## Implementation Plan

1. **Add `UserAgent` global to `version` package** - add a `UserAgent` variable (default `"solidping.io"`) that checkers can read
2. **Add `SP_USERAGENT` to config** - add `UserAgent` field to `Config`, load from `SP_USERAGENT` env var, set `version.UserAgent` at startup
3. **Update HTTP checker** - use `version.UserAgent` instead of `"SolidPing/"+version.Version`
4. **Update SMTP checker** - use `version.UserAgent` as default EHLO domain instead of `"solidping.local"`
5. **Log at startup** - log the configured user-agent name
