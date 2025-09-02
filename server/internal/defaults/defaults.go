// Package defaults provides default configuration values used across the application.
//
// This package centralizes all default credentials and configuration values to ensure
// consistency across the codebase. These defaults are used by:
//   - CLI configuration
//   - Server startup initialization
//   - Test fixtures
//   - Documentation
//
// WARNING: These are development/demo credentials and should NEVER be used in production.
package defaults

const (
	// ServerURL is the default server URL for local development.
	ServerURL = "http://localhost:4000"

	// Organization is the default organization slug created during initial setup.
	Organization = "default"

	// Email is the default admin email created during initial setup.
	// This account is created automatically when the server starts with no organizations.
	Email = "admin@solidping.com"

	// Password is the default admin password created during initial setup.
	// WARNING: This is a development/demo password. Change it immediately in production.
	Password = "solidpass"
)
