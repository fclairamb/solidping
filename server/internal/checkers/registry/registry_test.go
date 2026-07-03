package registry

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
	"github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes"
)

func TestGetChecker(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		checkType checkerdef.CheckType
		wantFound bool
		wantType  checkerdef.CheckType
	}{
		{
			name:      "http checker exists",
			checkType: checkerdef.CheckTypeHTTP,
			wantFound: true,
			wantType:  checkerdef.CheckTypeHTTP,
		},
		{
			name:      "kubernetes checker exists",
			checkType: checkerdef.CheckTypeKubernetes,
			wantFound: true,
			wantType:  checkerdef.CheckTypeKubernetes,
		},
		{
			name:      "unknown checker",
			checkType: "unknown",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			checker, found := GetChecker(tt.checkType)
			if found != tt.wantFound {
				t.Errorf("GetChecker() found = %v, want %v", found, tt.wantFound)
			}
			if found && checker.Type() != tt.wantType {
				t.Errorf("GetChecker() type = %v, want %v", checker.Type(), tt.wantType)
			}
		})
	}
}

func TestParseConfig(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		checkType checkerdef.CheckType
		wantFound bool
		// assertType verifies the concrete config type when found; nil skips it.
		assertType func(t *testing.T, cfg checkerdef.Config)
	}{
		{
			name:      "http config exists",
			checkType: checkerdef.CheckTypeHTTP,
			wantFound: true,
			assertType: func(t *testing.T, cfg checkerdef.Config) {
				t.Helper()
				if _, ok := cfg.(*checkhttp.HTTPConfig); !ok {
					t.Errorf("ParseConfig() returned wrong type, want *checkhttp.HTTPConfig")
				}
			},
		},
		{
			name:      "kubernetes config exists",
			checkType: checkerdef.CheckTypeKubernetes,
			wantFound: true,
			assertType: func(t *testing.T, cfg checkerdef.Config) {
				t.Helper()
				if _, ok := cfg.(*checkkubernetes.KubernetesConfig); !ok {
					t.Errorf("ParseConfig() returned wrong type, want *checkkubernetes.KubernetesConfig")
				}
			},
		},
		{
			name:      "unknown config type",
			checkType: "unknown",
			wantFound: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config, found := ParseConfig(tt.checkType)
			if found != tt.wantFound {
				t.Errorf("ParseConfig() found = %v, want %v", found, tt.wantFound)
			}
			if found && tt.assertType != nil {
				tt.assertType(t, config)
			}
		})
	}
}
