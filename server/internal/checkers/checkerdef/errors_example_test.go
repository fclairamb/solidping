package checkerdef_test

import (
	"fmt"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// ExampleNewConfigError demonstrates how to create a config error for a specific parameter.
func ExampleNewConfigError() {
	err := checkerdef.NewConfigError("url", "must be a valid HTTP or HTTPS URL")
	fmt.Println(err)
	// Output: url: must be a valid HTTP or HTTPS URL
}

// ExampleNewConfigErrorf demonstrates how to create a config error with formatted message.
func ExampleNewConfigErrorf() {
	minPort := 1
	maxPort := 65535
	err := checkerdef.NewConfigErrorf("port", "must be between %d and %d", minPort, maxPort)
	fmt.Println(err)
	// Output: port: must be between 1 and 65535
}

// ExampleIsConfigError demonstrates how to check if an error is a ConfigError.
func ExampleIsConfigError() {
	// Simulate a validation function returning a ConfigError
	err := checkerdef.NewConfigError("timeout", "must be positive")

	// Check if it's a ConfigError
	if configErr := checkerdef.IsConfigError(err); configErr != nil {
		fmt.Printf("Parameter: %s, Message: %s\n", configErr.Parameter, configErr.Message)
	}
	// Output: Parameter: timeout, Message: must be positive
}

// ExampleConfigError demonstrates typical usage in a Validate method.
func ExampleConfigError_validate() {
	// This would typically be in a checker's Validate method
	validateURL := func(url string) error {
		if url == "" {
			return checkerdef.NewConfigError("url", "cannot be empty")
		}
		if len(url) > 2048 {
			return checkerdef.NewConfigErrorf("url", "cannot exceed %d characters", 2048)
		}
		return nil
	}

	// Test empty URL
	if err := validateURL(""); err != nil {
		fmt.Println(err)
	}

	// Test too long URL
	longURL := string(make([]byte, 3000))
	if err := validateURL(longURL); err != nil {
		fmt.Println(err)
	}

	// Output:
	// url: cannot be empty
	// url: cannot exceed 2048 characters
}
