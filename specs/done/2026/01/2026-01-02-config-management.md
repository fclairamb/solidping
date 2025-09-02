# Configuration Management

## Overview

Implement support for loading application configuration from a `config.yml` file. This allows environment-specific settings, API credentials, and integration keys to be managed outside of environment variables and code.

## Features

### Config File Loading
- **Default location**: `./config.yml` in the current working directory
- **Environment override**: Support `CONFIG_FILE` environment variable to specify a custom path
- **Required on startup**: If `config.yml` exists but is invalid, fail startup with a clear error
- **Optional if missing**: If `config.yml` doesn't exist, proceed with environment variables as fallback

### Configuration Sections

The config file is structured with top-level sections for different systems:

```yaml
slack:
  app_id: string
  client_id: string
  client_secret: string
  signing_secret: string
```

### Environment Variable Mapping

Configuration can be overridden by environment variables using the pattern: `SP_<SECTION>_<KEY>` (uppercase with underscores).

Examples:
- `SP_SLACK_APP_ID` → `slack.app_id`
- `SP_SLACK_CLIENT_ID` → `slack.client_id`

Precedence (highest to lowest):
1. Environment variables (with `SP_` prefix)
2. Values from `config.yml`
3. Compiled-in defaults (if any)

### Validation

- Invalid YAML syntax → fail startup with parsing error
- Missing required fields → fail startup with clear error message
- Extra/unknown keys → warning but continue (future compatibility)

### Error Messages

All configuration loading errors should include:
- What was being parsed (file path or env var)
- What went wrong (syntax error, missing field, type mismatch, etc.)
- Where the error is (line number for YAML)
- Suggestion for fix

## Implementation

### Config Loading Order

1. Load compiled-in defaults (if any)
2. Check `CONFIG_FILE` environment variable
   - If set, load from that path (fail if not found or invalid)
3. If `CONFIG_FILE` not set, check for `./config.yml`
   - If exists, load it (fail if invalid)
   - If missing, continue with previous values
4. Apply environment variable overrides (`SOLIDPING_*`)
5. Validate final configuration

### File Format

Use YAML 1.2 for the config file format. Support:
- Comments (lines starting with `#`)
- Nested mappings
- Lists (for multi-value settings if needed)
- Standard YAML types (strings, numbers, booleans)

### Initial Config File

A `config.yml` file should be provided in the repository root with:
- All supported sections documented with comments
- Example values or placeholders
- Links to where to find these values (e.g., Slack app management URL)
- Notes about which values are required vs. optional

## Testing

- Test valid config file loading
- Test invalid YAML syntax handling
- Test environment variable overrides
- Test missing config file scenarios
- Test priority/precedence rules
- Test error messages for clarity

## Related Specs

| Spec | Relationship |
|------|--------------|
| `specs/2025-12-29-slack-bot.md` | Slack configuration is the first integration using config file |
| `specs/2026-01-01-notifiers.md` | Notifications may also use config file for settings |
