package checks_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// TestGeneratedScriptRecordsOriginAIGenerate: saving a js config whose ai
// block carries a new generated_at records the version with origin
// ai_generate; any other edit stays the caller's (spec 2026-10-03-07).
func TestGeneratedScriptRecordsOriginAIGenerate(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	svc, dbSvc, _, org := setupPlaintextChecksService(t)
	ctx, user := versionsUser(t, dbSvc, "alice@acme.com")

	aiBlock := func(generatedAt string) map[string]any {
		return map[string]any{
			"prompt": "acme answers", "contract": []any{"GET / answers 200"},
			"model": "m", "generated_at": generatedAt,
		}
	}

	created, err := svc.CreateCheck(ctx, org.Slug, checks.CreateCheckRequest{
		Name: "ai-js", Slug: "ai-js", Type: "js",
		Config: map[string]any{"script": `return { status: "up" };`, "ai": aiBlock("2026-10-01T00:00:00Z")},
	})
	r.NoError(err)

	origins := func() []string {
		list, listErr := svc.ListCheckVersions(ctx, org.Slug, created.UID, 0)
		r.NoError(listErr)

		out := make([]string, 0, len(list.Data))
		for _, version := range list.Data { // newest first
			out = append(out, version.Origin)
		}

		return out
	}

	r.Equal([]string{string(models.CheckVersionOriginAIGenerate)}, origins())

	list, err := svc.ListCheckVersions(ctx, org.Slug, created.UID, 0)
	r.NoError(err)
	r.Equal(user.UID, *list.Data[0].ActorUserUID, "the actor stays the caller")

	// A hand edit of the same generated script is the user's.
	same := map[string]any{"script": `return { status: "up", output: {} };`, "ai": aiBlock("2026-10-01T00:00:00Z")}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &same})
	r.NoError(err)

	// A regenerated script is ai_generate again.
	regenerated := map[string]any{"script": `return { status: "up" };`, "ai": aiBlock("2026-10-02T00:00:00Z")}
	_, err = svc.UpdateCheck(ctx, org.Slug, created.UID, &checks.UpdateCheckRequest{Config: &regenerated})
	r.NoError(err)

	r.Equal([]string{
		string(models.CheckVersionOriginAIGenerate),
		string(models.CheckVersionOriginUser),
		string(models.CheckVersionOriginAIGenerate),
	}, origins())
}
