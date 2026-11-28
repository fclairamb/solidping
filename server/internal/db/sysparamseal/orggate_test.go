package sysparamseal_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
)

// TestOrgSealGateDefersSweepAndWrites covers spec 2026-11-28-01: while a live
// reader cannot open `$sp_sealed`, the sweep seals nothing and a secret write
// stays plaintext; once the gate opens, the sweep seals them.
func TestOrgSealGateDefersSweepAndWrites(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	inner := &fakeOrgDB{params: []*models.Parameter{plainSecret("usr.a")}}
	svc := sysparamseal.Wrap(inner, &fakeSealer{enabled: true}, nil)

	blockers := []string{"eu-west-1 (0.55.0)"}
	svc.SetOrgSealGate(func(context.Context) ([]string, error) { return blockers, nil })

	sealed, err := svc.SealPlaintextOrgParameters(ctx)
	r.ErrorIs(err, sysparamseal.ErrOrgSealDeferred)
	r.ErrorContains(err, "eu-west-1 (0.55.0)")
	r.Zero(sealed)
	r.Nil(inner.stored, "nothing is rewritten while the gate is closed")
	r.True(svc.OrgSealPending())

	r.NoError(svc.SetOrgParameter(ctx, "org-1", "usr.b", "clear", true))
	r.Equal("clear", inner.stored, "a secret write stays plaintext while the gate is closed")

	var deferred *sysparamseal.OrgSealDeferredError
	r.ErrorAs(err, &deferred)
	r.Equal(blockers, deferred.Blockers)

	// The gate opens: the sweep seals, the flag clears, writes seal again.
	blockers = nil

	sealed, err = svc.SealPlaintextOrgParameters(ctx)
	r.NoError(err)
	r.Equal(1, sealed)
	r.False(svc.OrgSealPending())

	r.NoError(svc.SetOrgParameter(ctx, "org-1", "usr.b", "clear", true))
	stored, ok := inner.stored.(map[string]any)
	r.True(ok)
	r.Contains(stored, sysparamseal.SealedMarkerKey)
}

// TestOrgSealGateErrorDefers: a gate that cannot answer (the workers cannot be
// listed) defers too: plaintext is safe for every reader, a seal is not.
func TestOrgSealGateErrorDefers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	inner := &fakeOrgDB{params: []*models.Parameter{plainSecret("usr.a")}}
	svc := sysparamseal.Wrap(inner, &fakeSealer{enabled: true}, nil)
	svc.SetOrgSealGate(func(context.Context) ([]string, error) { return nil, errBoom })

	sealed, err := svc.SealPlaintextOrgParameters(ctx)
	r.ErrorIs(err, sysparamseal.ErrOrgSealDeferred)
	r.ErrorIs(err, errBoom)
	r.Zero(sealed)

	r.NoError(svc.SetOrgParameter(ctx, "org-1", "usr.b", "clear", true))
	r.Equal("clear", inner.stored)
	r.True(svc.OrgSealPending())
}

// TestOrgSealGateIgnoredWithoutKey: without a master key nothing is sealed and
// the gate is not even asked.
func TestOrgSealGateIgnoredWithoutKey(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	inner := &fakeOrgDB{params: []*models.Parameter{plainSecret("usr.a")}}
	svc := sysparamseal.Wrap(inner, &fakeSealer{enabled: false}, nil)
	svc.SetOrgSealGate(func(context.Context) ([]string, error) {
		t.Fatal("the gate must not be asked without a master key")

		return nil, nil
	})

	sealed, err := svc.SealPlaintextOrgParameters(ctx)
	r.NoError(err)
	r.Zero(sealed)
	r.NoError(svc.SetOrgParameter(ctx, "org-1", "usr.b", "clear", true))
	r.False(svc.OrgSealPending())
}
