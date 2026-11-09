package availability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/httpx"
)

var errChecksLookup = errors.New("checks lookup failed")

// failingChecksDB makes the batch check lookup fail (optionally after canceling
// the request context, to fail the later bucket query).
type failingChecksDB struct {
	db.Service

	cancel context.CancelFunc
}

func (d *failingChecksDB) GetChecksByUIDs(
	ctx context.Context, orgUID string, checkUIDs []string,
) (map[string]*models.Check, error) {
	if d.cancel != nil {
		checks, err := d.Service.GetChecksByUIDs(ctx, orgUID, checkUIDs)
		d.cancel()

		return checks, err
	}

	return nil, errChecksLookup
}

func TestGetOrgAvailabilityBuckets_ServiceErrors(t *testing.T) {
	t.Parallel()

	f := newBucketsFixture(t, "org-bkt-err")
	now := time.Now().UTC()
	valid := func(uids ...string) *GetOrgAvailabilityBucketsOptions {
		return &GetOrgAvailabilityBucketsOptions{
			From: now.Add(-3 * time.Hour), To: now, Bucket: time.Hour, CheckUIDs: uids,
		}
	}

	t.Run("invalid window", func(t *testing.T) {
		t.Parallel()

		opts := valid(f.check.UID)
		opts.Bucket = time.Minute

		_, err := f.svc.GetOrgAvailabilityBuckets(t.Context(), f.org.Slug, opts)
		require.ErrorIs(t, err, ErrInvalidWindow)
	})

	t.Run("unknown org", func(t *testing.T) {
		t.Parallel()

		_, err := f.svc.GetOrgAvailabilityBuckets(t.Context(), "no-such-org", valid(f.check.UID))
		require.ErrorIs(t, err, ErrOrganizationNotFound)
	})

	t.Run("no checks requested", func(t *testing.T) {
		t.Parallel()

		resp, err := f.svc.GetOrgAvailabilityBuckets(t.Context(), f.org.Slug, valid())
		require.NoError(t, err)
		require.Empty(t, resp.Data)
		require.Equal(t, 3600, resp.BucketSeconds)
	})

	t.Run("only foreign uids", func(t *testing.T) {
		t.Parallel()

		resp, err := f.svc.GetOrgAvailabilityBuckets(t.Context(), f.org.Slug, valid("not-mine"))
		require.NoError(t, err)
		require.Empty(t, resp.Data)
	})

	t.Run("checks lookup error", func(t *testing.T) {
		t.Parallel()

		svc := NewService(&failingChecksDB{Service: f.db}, f.svc.cfg)

		_, err := svc.GetOrgAvailabilityBuckets(t.Context(), f.org.Slug, valid(f.check.UID))
		require.ErrorIs(t, err, errChecksLookup)
	})

	t.Run("bucket query error", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		svc := NewService(&failingChecksDB{Service: f.db, cancel: cancel}, f.svc.cfg)

		_, err := svc.GetOrgAvailabilityBuckets(ctx, f.org.Slug, valid(f.check.UID))
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestGetOrgAvailabilityBuckets_NoDurationOmitsLatest(t *testing.T) {
	t.Parallel()

	f := newBucketsFixture(t, "org-bkt-nodur")
	now := time.Now().UTC()

	resp, err := f.svc.GetOrgAvailabilityBuckets(t.Context(), f.org.Slug, &GetOrgAvailabilityBucketsOptions{
		From: now.Add(-3 * time.Hour), To: now, Bucket: time.Hour, CheckUIDs: []string{f.check.UID},
	})
	require.NoError(t, err)
	require.Contains(t, resp.Data, f.check.UID)
	require.Nil(t, resp.Data[f.check.UID].LatestDurationMs)
}

func TestParseBucketParam(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]time.Duration{
		"": 0, "3600": time.Hour, "2h": 2 * time.Hour,
	} {
		got, err := parseBucketParam(raw)
		require.NoError(t, err, raw)
		require.Equal(t, want, got, raw)
	}

	_, err := parseBucketParam("soon")
	require.Error(t, err)
}

func TestHandlerGetOrgAvailabilityBuckets(t *testing.T) {
	t.Parallel()

	f := newBucketsFixture(t, "org-bkt-http")
	handler := NewHandler(f.svc, f.svc.cfg)

	router := httpx.New()
	router.GET("/api/v1/orgs/:org/availability/buckets", handler.GetOrgAvailabilityBuckets)

	now := time.Now().UTC().Truncate(time.Hour)
	f.raw(t, now.Add(-2*time.Hour+time.Minute), models.ResultStatusUp, "", false)

	window := func(from, to time.Time) url.Values {
		return url.Values{"from": {from.Format(time.RFC3339)}, "to": {to.Format(time.RFC3339)}}
	}
	call := func(org string, query url.Values) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			"/api/v1/orgs/"+org+"/availability/buckets?"+query.Encode(), nil)
		router.ServeHTTP(rec, req)

		return rec
	}

	t.Run("ok", func(t *testing.T) {
		t.Parallel()

		query := window(now.Add(-4*time.Hour), now)
		query.Set("bucket", "3600")
		query.Set("checks", " "+f.check.UID+" ,, ")

		rec := call(f.org.Slug, query)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		var body ListOrgAvailabilityBucketsResponse
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Len(t, body.Data, 1)
		require.Len(t, body.Data[f.check.UID].Buckets, 4)
		require.NotNil(t, body.Data[f.check.UID].LatestDurationMs)
	})

	t.Run("missing from", func(t *testing.T) {
		t.Parallel()

		rec := call(f.org.Slug, url.Values{"to": {now.Format(time.RFC3339)}})
		require.Equal(t, http.StatusBadRequest, rec.Code)
	})

	t.Run("malformed to", func(t *testing.T) {
		t.Parallel()

		query := window(now.Add(-time.Hour), now)
		query.Set("to", "yesterday")

		require.Equal(t, http.StatusBadRequest, call(f.org.Slug, query).Code)
	})

	t.Run("malformed bucket", func(t *testing.T) {
		t.Parallel()

		query := window(now.Add(-time.Hour), now)
		query.Set("bucket", "soon")

		rec := call(f.org.Slug, query)
		require.Equal(t, http.StatusBadRequest, rec.Code)
		require.Contains(t, rec.Body.String(), "Invalid bucket")
	})

	t.Run("invalid window", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, http.StatusBadRequest, call(f.org.Slug, window(now, now)).Code)
	})

	t.Run("too many checks", func(t *testing.T) {
		t.Parallel()

		uids := make([]string, MaxOrgBucketChecks+1)
		for i := range uids {
			uids[i] = fmt.Sprintf("uid-%d", i)
		}

		query := window(now.Add(-time.Hour), now)
		query.Set("checks", strings.Join(uids, ","))

		require.Equal(t, http.StatusBadRequest, call(f.org.Slug, query).Code)
	})

	t.Run("unknown org", func(t *testing.T) {
		t.Parallel()

		rec := call("no-such-org", window(now.Add(-time.Hour), now))
		require.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("internal error", func(t *testing.T) {
		t.Parallel()

		failing := NewHandler(NewService(&failingChecksDB{Service: f.db}, f.svc.cfg), f.svc.cfg)
		failRouter := httpx.New()
		failRouter.GET("/api/v1/orgs/:org/availability/buckets", failing.GetOrgAvailabilityBuckets)

		query := window(now.Add(-time.Hour), now)
		query.Set("checks", f.check.UID)

		rec := httptest.NewRecorder()
		failRouter.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet,
			"/api/v1/orgs/"+f.org.Slug+"/availability/buckets?"+query.Encode(), nil))
		require.Equal(t, http.StatusInternalServerError, rec.Code)
	})
}
