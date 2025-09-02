package checkerdef

import (
	"testing"
)

func TestConfigError_Error(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configErr *ConfigError
		want      string
	}{
		{
			name: "with parameter name",
			configErr: &ConfigError{
				Parameter: "url",
				Message:   "must be a valid HTTP or HTTPS URL",
			},
			want: "url: must be a valid HTTP or HTTPS URL",
		},
		{
			name: "without parameter name",
			configErr: &ConfigError{
				Parameter: "",
				Message:   "invalid configuration",
			},
			want: "invalid configuration",
		},
		{
			name: "with empty message",
			configErr: &ConfigError{
				Parameter: "timeout",
				Message:   "",
			},
			want: "timeout: ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := tt.configErr.Error(); got != tt.want {
				t.Errorf("ConfigError.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewConfigError(t *testing.T) {
	t.Parallel()

	err := NewConfigError("port", "must be between 1 and 65535")

	if err == nil {
		t.Fatal("NewConfigError() returned nil")
	}

	configErr := IsConfigError(err)
	if configErr == nil {
		t.Fatalf("NewConfigError() did not return *ConfigError, got %T", err)
	}

	if configErr.Parameter != "port" {
		t.Errorf("Parameter = %v, want %v", configErr.Parameter, "port")
	}

	if configErr.Message != "must be between 1 and 65535" {
		t.Errorf("Message = %v, want %v", configErr.Message, "must be between 1 and 65535")
	}

	expectedError := "port: must be between 1 and 65535"
	if configErr.Error() != expectedError {
		t.Errorf("Error() = %v, want %v", configErr.Error(), expectedError)
	}
}

func TestNewConfigErrorf(t *testing.T) {
	t.Parallel()

	minTimeout := 1
	maxTimeout := 300

	err := NewConfigErrorf("timeout", "must be between %d and %d seconds", minTimeout, maxTimeout)

	if err == nil {
		t.Fatal("NewConfigErrorf() returned nil")
	}

	configErr := IsConfigError(err)
	if configErr == nil {
		t.Fatalf("NewConfigErrorf() did not return *ConfigError, got %T", err)
	}

	if configErr.Parameter != "timeout" {
		t.Errorf("Parameter = %v, want %v", configErr.Parameter, "timeout")
	}

	expectedMessage := "must be between 1 and 300 seconds"
	if configErr.Message != expectedMessage {
		t.Errorf("Message = %v, want %v", configErr.Message, expectedMessage)
	}

	expectedError := "timeout: must be between 1 and 300 seconds"
	if configErr.Error() != expectedError {
		t.Errorf("Error() = %v, want %v", configErr.Error(), expectedError)
	}
}

var errOtherError = &ConfigError{Parameter: "other", Message: "some other error"}

func TestIsConfigError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		err     error
		want    *ConfigError
		wantNil bool
	}{
		{
			name: "is config error",
			err:  NewConfigError("url", "invalid URL"),
			want: &ConfigError{
				Parameter: "url",
				Message:   "invalid URL",
			},
			wantNil: false,
		},
		{
			name:    "is not config error",
			err:     errOtherError,
			want:    errOtherError,
			wantNil: false,
		},
		{
			name:    "nil error",
			err:     nil,
			want:    nil,
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := IsConfigError(tt.err)

			if tt.wantNil {
				if got != nil {
					t.Errorf("IsConfigError() = %v, want nil", got)
				}

				return
			}

			if got == nil {
				t.Fatal("IsConfigError() returned nil, expected ConfigError")
			}

			if got.Parameter != tt.want.Parameter {
				t.Errorf("Parameter = %v, want %v", got.Parameter, tt.want.Parameter)
			}

			if got.Message != tt.want.Message {
				t.Errorf("Message = %v, want %v", got.Message, tt.want.Message)
			}
		})
	}
}

func TestConfigError_AsError(t *testing.T) {
	t.Parallel()

	// Test that ConfigError can be used as a regular error
	err := NewConfigError("host", "cannot be empty")

	if err.Error() != "host: cannot be empty" {
		t.Errorf("Error() = %v, want %v", err.Error(), "host: cannot be empty")
	}
}
