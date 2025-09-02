# Checkers Package

This package implements the protocol checker system for SolidPing monitoring.

## Package Structure

### `checkerdef/` - Core interfaces and types
- **`Checker` interface**: `Type()`, `Validate(config)`, `Execute(ctx, config)`
- **`Config` interface**: `FromMap(map)`, `GetConfig()`
- **Common types**: `CheckType`, `Status` (Up/Down/Timeout/Error), `Result`

### `check{protocol}/` - Protocol implementations
Each protocol has its own package (e.g., `checkhttp`, `checkicmp`, `checktcp`):
- **`{Protocol}Config` struct**: Protocol-specific configuration implementing `Config` interface
- **`{Protocol}Checker` struct**: Protocol checker implementing `Checker` interface
- **Validation**: Protocol-specific validation logic
- **Execution**: Protocol-specific check execution

### `registry/` - Factory pattern
- **`GetChecker(checkType)`**: Returns checker implementation for a type
- **`ParseConfig(checkType)`**: Returns appropriate config struct for a type

## Adding New Checkers

1. Create `check{protocol}/` package
2. Define `{Protocol}Config` struct implementing `checkerdef.Config`
3. Implement `{Protocol}Checker` struct implementing `checkerdef.Checker`
4. Register in `registry/registry.go`:
   - Add to `GetChecker()` switch
   - Add to `ParseConfig()` switch
5. Add constant to `checkerdef.CheckType`
6. Run the lint and tests and fix any issues
