# Configuration Error Handling

This document describes the `ConfigError` type for reporting configuration validation errors with parameter-specific information.

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
