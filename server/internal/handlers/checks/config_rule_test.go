package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// A missing required parameter carries its rule, so the dashboard can show
// "required" in the reader's language instead of the raw "host: is required".
func TestValidateReportsTheRequiredRule(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	env := newWarnEnv(t)

	resp, err := env.svc.ValidateCheck(t.Context(), env.org.Slug, &checks.ValidateCheckRequest{
		Type:   "vnc",
		Slug:   "vnc-no-host",
		Config: map[string]any{"port": 5900},
	})
	r.NoError(err)

	var hostField *string

	for i := range resp.Fields {
		if resp.Fields[i].Name == "host" {
			hostField = &resp.Fields[i].Rule
		}
	}

	r.NotNil(hostField, "the missing host must be reported on the host field: %+v", resp.Fields)
	r.Equal(checkerdef.ConfigRuleRequired, *hostField)
}
