package incidentpublications_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentpublications"
)

var errInjected = errors.New("injected database failure")

// faultyDB wraps a real database and fails or alters the few reads and writes
// the check-deletion paths depend on.
type faultyDB struct {
	db.Service

	listErr     error
	updateErr   error
	incidentErr error
	checkErr    error
	// checkGone makes the check lookup return (nil, nil).
	checkGone bool
	hidePage  bool
	// incidentMut rewrites the incident the service reads.
	incidentMut func(*models.Incident)
	// afterList runs after a successful publication listing.
	afterList func()
}

func (f *faultyDB) ListIncidentPublications(
	ctx context.Context, filter *models.ListIncidentPublicationsFilter,
) ([]*models.IncidentPublication, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}

	pubs, err := f.Service.ListIncidentPublications(ctx, filter)
	if err == nil && f.afterList != nil {
		f.afterList()
	}

	return pubs, err
}

func (f *faultyDB) UpdateIncidentPublication(
	ctx context.Context, uid string, update *models.IncidentPublicationUpdate,
) error {
	if f.updateErr != nil {
		return f.updateErr
	}

	return f.Service.UpdateIncidentPublication(ctx, uid, update)
}

func (f *faultyDB) GetIncident(ctx context.Context, orgUID, uid string) (*models.Incident, error) {
	if f.incidentErr != nil {
		return nil, f.incidentErr
	}

	incident, err := f.Service.GetIncident(ctx, orgUID, uid)
	if err == nil && incident != nil && f.incidentMut != nil {
		f.incidentMut(incident)
	}

	return incident, err
}

func (f *faultyDB) GetCheckIncludingDeleted(ctx context.Context, orgUID, checkUID string) (*models.Check, error) {
	if f.checkErr != nil {
		return nil, f.checkErr
	}

	if f.checkGone {
		return nil, nil //nolint:nilnil // models a lookup that finds nothing without an error.
	}

	return f.Service.GetCheckIncludingDeleted(ctx, orgUID, checkUID)
}

func (f *faultyDB) GetStatusPage(ctx context.Context, orgUID, uid string) (*models.StatusPage, error) {
	if f.hidePage {
		return nil, sql.ErrNoRows
	}

	return f.Service.GetStatusPage(ctx, orgUID, uid)
}

// orphanScenario builds an open publication whose check was deleted the
// pre-hook way, and returns a service reading through the given faulty wrapper.
func orphanScenario(t *testing.T, fault *faultyDB) (*delSetup, *models.Incident, *incidentpublications.Service) {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	s := newDelSetup(t, dbSvc, models.AutoResolveIfUntouched)
	check := s.addCheck("payments-api", "Payments API", nil)
	incident := s.open(check)
	s.softDeleteOnly(check)

	require.False(t, s.onlyPublication().IsResolved(), "precondition: the entry is left open")

	fault.Service = dbSvc

	resolved, err := dbSvc.GetIncident(t.Context(), s.org.UID, incident.UID)
	require.NoError(t, err)

	return s, resolved, incidentpublications.NewService(fault, s.clk, nil)
}

func TestOnIncidentCheckDeleted_Faults(t *testing.T) {
	t.Parallel()

	t.Run("nil incident is ignored", func(t *testing.T) {
		t.Parallel()

		s, _, svc := orphanScenario(t, &faultyDB{})
		svc.OnIncidentCheckDeleted(t.Context(), nil)

		require.False(t, s.onlyPublication().IsResolved())
	})

	t.Run("a vanished status page leaves the entry alone", func(t *testing.T) {
		t.Parallel()

		s, incident, svc := orphanScenario(t, &faultyDB{hidePage: true})
		svc.OnIncidentCheckDeleted(t.Context(), incident)

		require.False(t, s.onlyPublication().IsResolved())
	})

	t.Run("a failing publication listing leaves the entry alone", func(t *testing.T) {
		t.Parallel()

		s, incident, svc := orphanScenario(t, &faultyDB{listErr: errInjected})
		svc.OnIncidentCheckDeleted(t.Context(), incident)

		require.False(t, s.onlyPublication().IsResolved())
	})

	t.Run("a failing update leaves the entry open and posts nothing", func(t *testing.T) {
		t.Parallel()

		s, incident, svc := orphanScenario(t, &faultyDB{updateErr: errInjected})
		before := len(s.updates(s.onlyPublication()))

		svc.OnIncidentCheckDeleted(t.Context(), incident)

		pub := s.onlyPublication()
		require.False(t, pub.IsResolved())
		require.Len(t, s.updates(pub), before)
		require.False(t, s.hasRemovedUpdate(pub))
	})
}

func TestCloseOrphanedPublications_Faults(t *testing.T) {
	t.Parallel()

	t.Run("a listing failure is reported", func(t *testing.T) {
		t.Parallel()

		_, _, svc := orphanScenario(t, &faultyDB{listErr: errInjected})

		closed, err := svc.CloseOrphanedPublications(t.Context())
		require.ErrorIs(t, err, errInjected)
		require.Zero(t, closed)
	})

	t.Run("a cancelled context stops the sweep before anything is closed", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		s, _, svc := orphanScenario(t, &faultyDB{afterList: cancel})

		closed, err := svc.CloseOrphanedPublications(ctx)
		require.ErrorIs(t, err, context.Canceled)
		require.Zero(t, closed)
		require.False(t, s.onlyPublication().IsResolved())
	})

	skipped := map[string]*faultyDB{
		"incident lookup fails": {incidentErr: errInjected},
		"incident still open": {incidentMut: func(i *models.Incident) {
			i.State = models.IncidentStateActive
		}},
		"incident has no check": {incidentMut: func(i *models.Incident) { i.CheckUID = "" }},
		"check lookup fails":    {checkErr: errInjected},
		"status page is gone":   {hidePage: true},
		"update fails":          {updateErr: errInjected},
	}

	for name, fault := range skipped {
		t.Run("skips the entry when "+name, func(t *testing.T) {
			t.Parallel()

			s, _, svc := orphanScenario(t, fault)

			closed, err := svc.CloseOrphanedPublications(t.Context())
			require.NoError(t, err)
			require.Zero(t, closed)
			require.False(t, s.onlyPublication().IsResolved())
		})
	}

	closing := map[string]*faultyDB{
		"check row is gone for good": {checkErr: sql.ErrNoRows},
		"check lookup finds nothing": {checkGone: true},
	}

	for name, fault := range closing {
		t.Run("closes the entry when the "+name, func(t *testing.T) {
			t.Parallel()

			s, _, svc := orphanScenario(t, fault)

			closed, err := svc.CloseOrphanedPublications(t.Context())
			require.NoError(t, err)
			require.Equal(t, 1, closed)
			require.True(t, s.onlyPublication().IsResolved())
			require.True(t, s.hasRemovedUpdate(s.onlyPublication()))
		})
	}
}
