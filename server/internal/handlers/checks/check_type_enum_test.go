package checks

import (
	"encoding/json"
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

// The enums list the built-in types only, but the generated client never
// calls Valid() when it decodes: a plugin type (`x-` prefix, spec
// 2026-10-17-05) round-trips through it. openapi.yaml documents this on each
// enum. The enum stays closed so the client keeps its typed constants.
func TestGeneratedClientAcceptsPluginTypes(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	var item client.CheckListItem
	r.NoError(json.Unmarshal([]byte(`{"type":"x-gopher"}`), &item))
	r.NotNil(item.Type)
	r.Equal(client.CheckListItemType("x-gopher"), *item.Type)

	body, err := json.Marshal(client.CreateCheckRequest{Type: new(client.CreateCheckRequestType("x-gopher"))})
	r.NoError(err)
	r.Contains(string(body), `"type":"x-gopher"`)
}
