# Checker Error Catalogue

Two distinct kinds of error live in `checkerdef`, and they are catalogued
separately because they surface in different places:

1. **Write-time configuration errors** (`ConfigError`) — raised by a checker's
   `Validate` or a shared config gate, surfaced as a `400`/`422`
   `VALIDATION_ERROR` on a named field. Everything below the
   [Configuration errors](#configuration-errors) heading.
2. **Run-time check failures** — raised while a check executes, surfaced as the
   `error` key of the result output and read by an operator staring at a red
   check. These carry a **static sentinel** so `errors.Is` can classify them, a
   message naming the target and the fix, and a defined result status.

Add a new run-time sentinel to the table below whenever a failure is
*user-actionable in its own way* — i.e. when reporting it as a generic "the
target is down" would send the user looking in the wrong place.

## Run-time failure catalogue

| Sentinel | Status | Fires when | Message shape |
|---|---|---|---|
| `ErrNoAddressForFamily` (`ipversion.go`) | `StatusDown` | A check pinned with `ipVersion: ipv4`/`ipv6` resolves its target and finds no address of that family — usually a missing AAAA record. Also fires for the `dnsbl`/`http` literal-address variants. | `no address of the requested IP version: <host> has no <IPv6> address (this check is pinned to <IPv6>; remove the ipVersion option or publish an <AAAA> record for it)` |
| `ErrWorkerNoEgress` (`ipversion.go`) | `StatusError` | The pinned family cannot be originated by **this worker** at all — the node has no IPv6 route. Detected by a zero-packet connected-UDP route lookup before dialing. | `worker has no egress for the requested IP version: this worker has no <IPv6> connectivity, so the check could not even be sent — the target is probably fine; run this check from a region with <IPv6> egress, or set ipVersion back to auto (<cause>)` |
| `errTargetIPv6Unsupported` (`checkdnsbl`) | `StatusError` | A `dnsbl` check carries `ipVersion: ipv6`. Rejected at write time, so this only fires on a hand-edited row — but it must still say why rather than dying inside a reverse-octet helper. Not the target's fault, hence `StatusError`. | `dnsbl checks query IPv4 blocklists only, so this check cannot be pinned to IPv6: "<target>"` |

Conventions these follow, and that a new entry must follow too:

- **Static sentinel, wrapped with `%w`.** Callers classify with `errors.Is`; the
  `err113` linter rejects a dynamic `errors.New` at the raise site anyway.
- **Name the target and the fix in the message.** "No AAAA record" is
  actionable; "dial failed" is not.
- **Distinguish "the target is broken" from "we are broken".** These two
  sentinels exist as a *pair* precisely so a worker-side infrastructure gap is
  never reported as the user's service being down. That is also why their
  statuses differ.
- **Map the status in one place.** `checkerdef.ResolveFailureStatus(err,
  fallback)` (and its `IPVersionFailureStatus` shorthand) is the single mapping,
  so the same condition reports the same status on every check type — `Down` and
  `Error` account differently for availability, and a per-checker verdict would
  make a user's uptime number depend on which check type they picked. The
  `fallback` preserves whatever status that checker already reported for
  unrelated resolve failures.

## Configuration errors

The rest of this document describes the `ConfigError` type for reporting configuration validation errors with parameter-specific information.

## Overview

`ConfigError` provides structured error information that identifies which configuration parameter failed validation and why. This is particularly useful for:

- API responses that need to display field-specific error messages
- Frontend forms that need to highlight specific input fields
- Better error messages for configuration debugging

## Basic Usage

### Creating a ConfigError

```go
// Simple error for a parameter
return checkerdef.NewConfigError("url", "must be a valid HTTP or HTTPS URL")

// Formatted error message
return checkerdef.NewConfigErrorf("port", "must be between %d and %d", 1, 65535)
```

### Checking if an error is a ConfigError

```go
if configErr := checkerdef.IsConfigError(err); configErr != nil {
    // Access the parameter name and message
    log.Printf("Parameter: %s, Message: %s", configErr.Parameter, configErr.Message)
}
```

## Example: Checker Validation

Here's how a checker should use `ConfigError` in its `Validate` method:

```go
func (c *MyChecker) Validate(config checkerdef.Config) error {
    cfg, ok := config.(*MyConfig)
    if !ok {
        return errors.New("invalid config type")
    }

    // Validate URL parameter
    if cfg.URL == "" {
        return checkerdef.NewConfigError("url", "cannot be empty")
    }

    if !strings.HasPrefix(cfg.URL, "http://") && !strings.HasPrefix(cfg.URL, "https://") {
        return checkerdef.NewConfigError("url", "must start with http:// or https://")
    }

    // Validate timeout with formatted message
    if cfg.Timeout < 1 || cfg.Timeout > 300 {
        return checkerdef.NewConfigErrorf("timeout", "must be between %d and %d seconds", 1, 300)
    }

    // Validate port range
    if cfg.Port < 1 || cfg.Port > 65535 {
        return checkerdef.NewConfigErrorf("port", "must be between %d and %d", 1, 65535)
    }

    return nil
}
```

## Example: FromMap Validation

When parsing configuration from a map in the `FromMap` method:

```go
func (c *MyConfig) FromMap(configMap map[string]any) error {
    // Type checking with ConfigError
    urlVal, ok := configMap["url"]
    if !ok {
        return checkerdef.NewConfigError("url", "is required")
    }

    urlStr, ok := urlVal.(string)
    if !ok {
        return checkerdef.NewConfigError("url", "must be a string")
    }

    c.URL = urlStr

    // Optional field with type checking
    if timeoutVal, ok := configMap["timeout"]; ok {
        timeoutFloat, ok := timeoutVal.(float64)
        if !ok {
            return checkerdef.NewConfigError("timeout", "must be a number")
        }
        c.Timeout = int(timeoutFloat)
    }

    return nil
}
```

## Error Format

The error message format is: `{parameter}: {message}`

Examples:
- `url: cannot be empty`
- `port: must be between 1 and 65535`
- `timeout: must be a positive number`

If no parameter is specified, only the message is returned.

## API Integration

Handlers can use `IsConfigError` to detect configuration errors and return structured API responses:

```go
if err := checker.Validate(config); err != nil {
    if configErr := checkerdef.IsConfigError(err); configErr != nil {
        // Return API error with field information
        return &api.ValidationError{
            Field:   configErr.Parameter,
            Message: configErr.Message,
        }
    }
    // Handle other error types
    return err
}
```

## Migration from Simple Errors

Existing checkers using simple errors can be gradually migrated:

**Before:**
```go
var ErrURLRequired = errors.New("url is required")
```

**After:**
```go
return checkerdef.NewConfigError("url", "is required")
```

This provides the same error message but with structured parameter information that can be used by API clients.
