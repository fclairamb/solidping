package registry

import (
	"testing"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkhttp"
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
	}{
		{
			name:      "http config exists",
			checkType: checkerdef.CheckTypeHTTP,
			wantFound: true,
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
			if found {
				if _, ok := config.(*checkhttp.HTTPConfig); !ok {
					t.Errorf("ParseConfig() returned wrong type, want *checkhttp.HTTPConfig")
				}
			}
		})
	}
}
