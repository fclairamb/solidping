# Sample Checks Provider

**Type**: feature
**Date**: 2025-12-18
**Status**: Ready for Implementation

## Quick Reference

**Key Actions**:
1. Implement `CheckerSamplesProvider` interface in all checker types (http, dns, ping, tcp)
2. Use sample configs on startup when `SP_RUN_MODE` is not specified
3. Auto-populate checks with sample configurations from all checkers
4. Support different sample types: Default, Demo, Test

**Key Interfaces**:
- `CheckerSamplesProvider.GetSampleConfigs(opts *ListSampleOptions) []SampleConfig`
- `SampleConfig` - Contains Name, Slug, and Config
- `ListSampleOptions.Type` - Default, Demo, or Test

## Idea

Add the ability for checkers to provide sample configurations that can be used to auto-populate checks on application startup, making it easier for users to get started with SolidPing.

## Description

This feature introduces a `CheckerSamplesProvider` interface that allows each checker type (HTTP, DNS, Ping, TCP) to provide sample configurations. When the application starts without a specific `SP_RUN_MODE` being set, it will automatically load sample checks from all registered checkers to demonstrate the capabilities of the system.

### Requirements

1. **CheckerSamplesProvider Interface** (already defined in `checkerdef/interface.go`):
   - Each checker that wants to provide samples should implement `CheckerSamplesProvider`
   - `GetSampleConfigs(opts *ListSampleOptions) []SampleConfig` - Returns a slice of sample configurations
   - `SampleConfig` structure contains:
     - `Name` - Human-readable name for the sample check
     - `Slug` - URL-friendly identifier for the sample check
     - `Config` - The actual checker configuration
   - The `ListSampleOptions` parameter allows filtering by type (Default, Demo, or Test)

2. **Implementation in Checkers**:
   - **HTTP Checker**: Provide samples for HTTP/HTTPS endpoints
     - Example: Check Google, health endpoint example, API endpoint example
   - **DNS Checker**: Provide samples for DNS resolution
     - Example: google.com A record, cloudflare.com resolution
   - **Ping Checker**: Provide samples for ICMP ping
     - Example: 8.8.8.8 (Google DNS), 1.1.1.1 (Cloudflare DNS)
   - **TCP Checker**: Provide samples for TCP port checks
     - Example: google.com:443, DNS port 53

3. **Startup Behavior**:
   - When `SP_RUN_MODE` is **not** specified, load samples with `ListSampleOptionType = Default`
   - When `SP_RUN_MODE=demo`, load samples with `ListSampleOptionType = Demo`
   - When `SP_RUN_MODE=test`, use test-specific data (no samples, use deterministic test checks)
   - Only create sample checks if the organization has no existing checks

4. **Sample Configuration Guidelines**:
   - Use publicly accessible endpoints (google.com, cloudflare.com, etc.)
   - Include diverse examples showing different checker capabilities
   - Keep sample names descriptive and clear
   - Ensure samples are valid and will execute successfully

## Acceptance Criteria

- [ ] HTTP checker implements `CheckerSamplesProvider` interface
- [ ] DNS checker implements `CheckerSamplesProvider` interface
- [ ] Ping checker implements `CheckerSamplesProvider` interface
- [ ] TCP checker implements `CheckerSamplesProvider` interface
- [ ] Each checker provides at least 2-3 sample configurations
- [ ] Startup job loads samples when `SP_RUN_MODE` is not specified
- [ ] Samples are only loaded if organization has zero existing checks
- [ ] Different sample types (Default, Demo, Test) are supported
- [ ] All sample configurations are valid and pass validation
- [ ] Samples use publicly accessible endpoints
- [ ] Sample checks execute successfully after creation
- [ ] All existing tests pass
- [ ] Linting passes
- [ ] Build succeeds

## Technical Considerations

- **Idempotency**: Only create sample checks if organization has no existing checks
- **Validation**: All sample configurations must pass the checker's `Validate()` method
- **Startup Performance**: Loading samples should be fast and not delay startup significantly
- **Database**: Sample checks should be created as regular checks in the database
- **Organization Scope**: Samples should be loaded for the default/demo organization
- **Check Slugs**: Generate unique slugs for sample checks (e.g., "sample-http-google", "sample-dns-cloudflare")

## Implementation Notes

### Architecture Decisions

1. **Interface Location**: `CheckerSamplesProvider` is already defined in `checkerdef/interface.go`
2. **Sample Types**: Use `ListSampleOptions.Type` to support Default, Demo, and Test variations
3. **Startup Integration**: Load samples in the startup job (`job_startup.go`)
4. **Conditional Loading**: Only load when `SP_RUN_MODE` is not set OR when explicitly requesting samples
5. **Check Creation**: Create as regular checks in the database using the checks service

### Files to Modify

#### Checker Implementations

- **`back/internal/checkers/checkhttp/checker.go`**:
  - Implement `GetSampleConfigs(opts *ListSampleOptions) []Config` method
  - Return 2-3 sample HTTP/HTTPS check configurations
  - Examples: Google homepage, HTTP health endpoint, HTTPS API endpoint

- **`back/internal/checkers/checkdns/checker.go`**:
  - Implement `GetSampleConfigs(opts *ListSampleOptions) []Config` method
  - Return 2-3 sample DNS resolution configurations
  - Examples: google.com A record, cloudflare.com resolution

- **`back/internal/checkers/checkping/checker.go`**:
  - Implement `GetSampleConfigs(opts *ListSampleOptions) []Config` method
  - Return 2-3 sample ICMP ping configurations
  - Examples: 8.8.8.8 (Google DNS), 1.1.1.1 (Cloudflare DNS)

- **`back/internal/checkers/checktcp/checker.go`**:
  - Implement `GetSampleConfigs(opts *ListSampleOptions) []Config` method
  - Return 2-3 sample TCP port check configurations
  - Examples: google.com:443, cloudflare.com:443

#### Startup Job

- **`back/internal/jobs/jobtypes/job_startup.go`**:
  - Add logic to detect if `SP_RUN_MODE` is empty or "demo"
  - Query if default organization has any existing checks
  - If no checks exist, load samples from all checkers:
    - Iterate through all check types: HTTP, DNS, Ping, TCP
    - Get the checker instance from registry
    - Check if checker implements `CheckerSamplesProvider` interface
    - Call `GetSampleConfigs()` with appropriate options
    - Create checks in database for each sample
  - Generate unique slugs for sample checks (e.g., "sample-http-{name}")
  - Set sample checks as enabled by default

#### Registry Support

- **`back/internal/checkers/registry/registry.go`**:
  - Ensure all checker instances are accessible
  - May need to add a `GetAllCheckers()` method to iterate through all checker types

### Sample Check Examples

#### HTTP Checker Samples
```go
func (c *HTTPChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.Config {
    samples := []checkerdef.Config{
        &HTTPConfig{
            URL:     "https://www.google.com",
            Method:  "GET",
            Timeout: 10 * time.Second,
        },
        &HTTPConfig{
            URL:     "https://api.github.com/status",
            Method:  "GET",
            Timeout: 10 * time.Second,
        },
    }

    if opts.Type == checkerdef.Demo {
        // Add additional demo-specific samples
    }

    return samples
}
```

#### DNS Checker Samples
```go
func (c *DNSChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.Config {
    return []checkerdef.Config{
        &DNSConfig{
            Domain:     "google.com",
            RecordType: "A",
            Timeout:    5 * time.Second,
        },
        &DNSConfig{
            Domain:     "cloudflare.com",
            RecordType: "A",
            Timeout:    5 * time.Second,
        },
    }
}
```

#### Ping Checker Samples
```go
func (c *PingChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.Config {
    return []checkerdef.Config{
        &PingConfig{
            Target:  "8.8.8.8",
            Count:   3,
            Timeout: 5 * time.Second,
        },
        &PingConfig{
            Target:  "1.1.1.1",
            Count:   3,
            Timeout: 5 * time.Second,
        },
    }
}
```

#### TCP Checker Samples
```go
func (c *TCPChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.Config {
    return []checkerdef.Config{
        &TCPConfig{
            Host:    "google.com",
            Port:    443,
            Timeout: 5 * time.Second,
        },
        &TCPConfig{
            Host:    "cloudflare.com",
            Port:    443,
            Timeout: 5 * time.Second,
        },
    }
}
```

### Startup Job Sample Loading Logic

```go
// In job_startup.go Execute() method
func loadSampleChecks(ctx context.Context, db db.Service, orgUID uuid.UUID) error {
    // Check if organization already has checks
    existingChecks, err := db.ListChecks(ctx, orgUID)
    if err != nil {
        return err
    }

    if len(existingChecks) > 0 {
        // Organization already has checks, skip sample loading
        return nil
    }

    // Determine sample type based on run mode
    sampleType := checkerdef.Default
    if runMode := os.Getenv("SP_RUN_MODE"); runMode == "demo" {
        sampleType = checkerdef.Demo
    }

    opts := &checkerdef.ListSampleOptions{Type: sampleType}

    // Load samples from all checkers
    checkTypes := checkerdef.ListCheckTypes(opts)
    for _, checkType := range checkTypes {
        checker := registry.GetChecker(checkType)

        // Check if checker implements SamplesProvider
        provider, ok := checker.(checkerdef.CheckerSamplesProvider)
        if !ok {
            continue
        }

        samples := provider.GetSampleConfigs(opts)
        for i, sampleConfig := range samples {
            // Create check from sample
            check := &models.Check{
                UID:             uuid.New(),
                OrganizationUID: orgUID,
                Name:            fmt.Sprintf("Sample %s Check %d", checkType, i+1),
                Slug:            fmt.Sprintf("sample-%s-%d", checkType, i+1),
                Type:            string(checkType),
                Config:          sampleConfig.GetConfig(),
                Enabled:         true,
                Period:          60 * time.Second,
            }

            if err := db.CreateCheck(ctx, check); err != nil {
                return err
            }
        }
    }

    return nil
}
```

### Testing Strategy

1. **Manual Testing**:
   - **Default Mode** (no `SP_RUN_MODE`):
     - Start fresh database
     - Verify sample checks are created for HTTP, DNS, Ping, TCP
     - Verify all samples are enabled and valid
     - Verify samples execute successfully
   - **Demo Mode** (`SP_RUN_MODE=demo`):
     - Start fresh database
     - Verify demo-specific samples are loaded
   - **Test Mode** (`SP_RUN_MODE=test`):
     - Verify samples are NOT loaded (test mode uses deterministic data)
   - **Existing Checks**:
     - Start with database that has existing checks
     - Verify samples are NOT loaded (idempotency)

2. **Unit Testing**:
   - Test each checker's `GetSampleConfigs()` implementation
   - Verify returned samples are valid configurations
   - Verify samples pass the checker's `Validate()` method
   - Test with different `ListSampleOptions` types

3. **Integration Testing**:
   - Test startup job sample loading logic
   - Verify samples are created in database correctly
   - Test idempotency (no duplicate samples on restart)

### Risk Assessment

**Low Risk**:
- Adding `GetSampleConfigs()` method is optional (interface is already optional)
- Samples are only loaded on fresh databases with zero checks
- Samples use public, stable endpoints
- Feature is conditional on `SP_RUN_MODE` configuration

**Mitigation**:
- Validate all sample configurations before saving to database
- Use try-catch around sample loading to prevent startup failures
- Log sample loading activity for debugging
- Make sample loading opt-in via configuration if needed

### Implementation Order

1. Implement `GetSampleConfigs()` in HTTP checker with 2-3 samples
2. Implement `GetSampleConfigs()` in DNS checker with 2-3 samples
3. Implement `GetSampleConfigs()` in Ping checker with 2-3 samples
4. Implement `GetSampleConfigs()` in TCP checker with 2-3 samples
5. Update startup job to load samples when appropriate
6. Add logic to check for existing checks (idempotency)
7. Test with fresh database in default mode
8. Test with fresh database in demo mode
9. Test that samples are not loaded when checks exist
10. Test that samples are not loaded in test mode
11. Verify all samples execute successfully
12. Run linting and tests

## Pre-Implementation Checklist

Before starting implementation, verify:
- [ ] You understand the `CheckerSamplesProvider` interface
- [ ] You understand the different `ListSampleOptionType` values
- [ ] You know which public endpoints to use for samples
- [ ] You understand when samples should and should not be loaded
- [ ] You have reviewed the startup job code

## Post-Implementation Verification

After implementation is complete:
- [ ] Start fresh database without `SP_RUN_MODE` - verify samples are created
- [ ] Check that each checker type has sample checks created
- [ ] Verify all sample checks are enabled
- [ ] Verify all sample checks execute successfully
- [ ] Restart server - verify no duplicate samples are created
- [ ] Test with `SP_RUN_MODE=demo` - verify demo samples work
- [ ] Test with `SP_RUN_MODE=test` - verify no samples are loaded
- [ ] Run `make lint` - should pass
- [ ] Run `make test` - all tests should pass
- [ ] Run `make build` - should succeed
