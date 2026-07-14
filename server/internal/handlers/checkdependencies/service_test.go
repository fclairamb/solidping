package checkdependencies

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

func TestCheckDependencyKindIsValid(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	r.True(models.CheckDependencyKindHard.IsValid())
	r.True(models.CheckDependencyKindSoft.IsValid())
	r.False(models.CheckDependencyKind("medium").IsValid())
	r.False(models.CheckDependencyKind("").IsValid())
}

func TestBuildDependencyResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	desc := "consumes the orders queue"
	dep := &models.CheckDependency{
		UID:            "edge-1",
		ParentCheckUID: "p1",
		ChildCheckUID:  "c1",
		Kind:           models.CheckDependencyKindHard,
		Description:    &desc,
	}

	checks := map[string]CheckRef{
		"p1": {UID: "p1", Slug: "rabbit", Name: "RabbitMQ"},
		"c1": {UID: "c1", Slug: "worker", Name: "Worker"},
	}

	resp := buildDependencyResponse(dep, checks)
	r.Equal("edge-1", resp.UID)
	r.Equal("rabbit", resp.ParentCheck.Slug)
	r.Equal("worker", resp.ChildCheck.Slug)
	r.Equal("hard", resp.Kind)
	r.Equal(&desc, resp.Description)
}

func TestResolvedDependencyResponse(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	dep := &models.CheckDependency{
		UID:            "edge-1",
		ParentCheckUID: "p1",
		ChildCheckUID:  "c1",
		Kind:           models.CheckDependencyKindHard,
	}

	t.Run("both endpoints resolved", func(t *testing.T) {
		t.Parallel()

		checks := map[string]CheckRef{
			"p1": {UID: "p1", Slug: "rabbit", Name: "RabbitMQ"},
			"c1": {UID: "c1", Slug: "worker", Name: "Worker"},
		}

		resp, ok := resolvedDependencyResponse(dep, checks)
		r.True(ok)
		r.Equal("edge-1", resp.UID)
		r.Equal("p1", resp.ParentCheck.UID)
		r.Equal("c1", resp.ChildCheck.UID)
	})

	// Reproduces issue #129: a dependency edge outlives the check it points
	// to (e.g. the parent/child check was deleted but the edge wasn't
	// cleaned up). loadCheckRefs then never populates that UID in the map,
	// so the lookup returns the CheckRef zero value. Previously this edge
	// was still rendered — a kind badge with no check name attached. It must
	// now be omitted entirely instead.
	t.Run("child check missing (deleted) is omitted, not rendered empty", func(t *testing.T) {
		t.Parallel()

		checks := map[string]CheckRef{
			"p1": {UID: "p1", Slug: "rabbit", Name: "RabbitMQ"},
			// "c1" absent: the child check was deleted and never resolved.
		}

		resp, ok := resolvedDependencyResponse(dep, checks)
		r.False(ok)
		r.Equal(DependencyResponse{}, resp)
	})

	t.Run("parent check missing (deleted) is omitted, not rendered empty", func(t *testing.T) {
		t.Parallel()

		checks := map[string]CheckRef{
			"c1": {UID: "c1", Slug: "worker", Name: "Worker"},
			// "p1" absent.
		}

		resp, ok := resolvedDependencyResponse(dep, checks)
		r.False(ok)
		r.Equal(DependencyResponse{}, resp)
	})
}

func TestCollectCheckUIDs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	parents := []*models.CheckDependency{
		{ParentCheckUID: "p1", ChildCheckUID: "c1"},
		{ParentCheckUID: "p2", ChildCheckUID: "c1"},
	}
	children := []*models.CheckDependency{
		{ParentCheckUID: "c1", ChildCheckUID: "g1"},
	}

	uids := collectCheckUIDs(parents, children)
	r.ElementsMatch([]string{"p1", "p2", "c1", "g1"}, uids)
}

func TestDerefString(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	s := "hello"
	r.Equal("hello", derefString(&s))
	r.Empty(derefString(nil))
}
