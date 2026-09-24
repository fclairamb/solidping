package checks

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/pkg/client"
)

// TestCheckTypeEnumsMatchTheRegistry pins the four check-type enums of
// openapi.yaml (through the generated client's Valid()) to the registry. They
// used to list six types, so a generated client rejected every heartbeat,
// email or database check the server returned (fixed with spec 2026-09-25-05,
// which adds private-location).
func TestCheckTypeEnumsMatchTheRegistry(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		name := string(checkType)

		r.Truef(client.CheckType(name).Valid(), "Check.type does not declare %q", name)
		r.Truef(client.CheckListItemType(name).Valid(), "CheckListItem.type does not declare %q", name)
		r.Truef(client.CreateCheckRequestType(name).Valid(), "CreateCheckRequest.type does not declare %q", name)
		r.Truef(client.UpsertCheckRequestType(name).Valid(), "UpsertCheckRequest.type does not declare %q", name)
	}

	r.False(client.CheckType("not-a-type").Valid(), "positive control: the enum is closed")
}
