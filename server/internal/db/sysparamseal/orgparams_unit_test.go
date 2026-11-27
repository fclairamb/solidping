package sysparamseal_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sysparamseal"
)

// fakeOrgDB overrides the org parameter methods of an otherwise nil db.Service.
type fakeOrgDB struct {
	db.Service

	params  []*models.Parameter
	orgsErr error
	listErr error
	getErr  error
	setErr  error
	stored  any
}

func (f *fakeOrgDB) ListOrganizations(context.Context) ([]*models.Organization, error) {
	return []*models.Organization{{UID: "org-1"}}, f.orgsErr
}

func (f *fakeOrgDB) ListOrgParameters(context.Context, string) ([]*models.Parameter, error) {
	return f.params, f.listErr
}

func (f *fakeOrgDB) ListOrgParametersByKey(context.Context, string) ([]*models.Parameter, error) {
	return f.params, f.listErr
}

func (f *fakeOrgDB) GetOrgParameter(_ context.Context, _, key string) (*models.Parameter, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}

	return findParam(f.params, key), nil
}

func (f *fakeOrgDB) SetOrgParameter(_ context.Context, _, _ string, value any, _ bool) error {
	f.stored = value

	return f.setErr
}

func TestOrgParameterErrorsPassThrough(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	inner := &fakeOrgDB{getErr: errBoom, listErr: errBoom}
	svc := sysparamseal.Wrap(inner, &fakeSealer{enabled: true}, nil)

	_, err := svc.GetOrgParameter(ctx, "org-1", "usr.a")
	r.ErrorIs(err, errBoom)

	_, err = svc.ListOrgParameters(ctx, "org-1")
	r.ErrorIs(err, errBoom)

	_, err = svc.ListOrgParametersByKey(ctx, "usr.a")
	r.ErrorIs(err, errBoom)

	_, err = svc.SealPlaintextOrgParameters(ctx)
	r.ErrorIs(err, errBoom)

	_, err = svc.PlaintextSecretOrgParameterCount(ctx)
	r.ErrorIs(err, errBoom)

	inner.orgsErr = errBoom
	_, err = svc.PlaintextSecretOrgParameterCount(ctx)
	r.ErrorContains(err, "list organizations")
}

func TestOrgParameterSealFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	inner := &fakeOrgDB{params: []*models.Parameter{plainSecret("usr.a")}}
	svc := sysparamseal.Wrap(inner, &fakeSealer{enabled: true, sealErr: errBoom}, nil)

	r.ErrorIs(svc.SetOrgParameter(ctx, "org-1", "usr.a", "v", true), errBoom)
	r.Nil(inner.stored, "nothing is written when sealing fails")

	sealed, err := svc.SealPlaintextOrgParameters(ctx)
	r.ErrorIs(err, errBoom)
	r.Zero(sealed)

	inner.setErr = errBoom
	svc = sysparamseal.Wrap(inner, &fakeSealer{enabled: true}, nil)

	sealed, err = svc.SealPlaintextOrgParameters(ctx)
	r.ErrorContains(err, "store sealed organization parameter")
	r.Zero(sealed)
}

func TestOrgParameterOpensOnlySealableRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	r := require.New(t)

	// A non-secret row whose value merely looks like an envelope is never opened.
	lookalike := sealedParam("usr.lookalike")
	lookalike.Secret = boolPtr(false)

	inner := &fakeOrgDB{params: []*models.Parameter{sealedParam("usr.a"), lookalike, nil}}
	svc := sysparamseal.Wrap(inner, &fakeSealer{enabled: true, opened: models.ParameterValue("clear")}, nil)

	listed, err := svc.ListOrgParameters(ctx, "org-1")
	r.NoError(err)
	r.Equal("clear", listed[0].Value[models.ParameterValueKey])
	r.Equal(lookalike, listed[1])

	inner.params = []*models.Parameter{sealedParam("usr.a")}
	svc = sysparamseal.Wrap(inner, &fakeSealer{enabled: true, openErr: errBoom}, nil)

	_, err = svc.ListOrgParametersByKey(ctx, "usr.a")
	r.ErrorIs(err, sysparamseal.ErrSealedUnavailable)
}
