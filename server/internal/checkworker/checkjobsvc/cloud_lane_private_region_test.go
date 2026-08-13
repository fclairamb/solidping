package checkjobsvc_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
)

// TestCloudLaneNeverClaimsPrivateRegionJobs is the cloud half of the cross-org
// security requirement of spec 2026-08-13-01. Private regions are now stored
// org-relatively (`@aws-paris`), so they are unique only WITHIN an org — while
// the cloud claim lane deliberately carries no organization predicate, because
// a cloud region is legitimately shared across every org. The lane must
// therefore never see an `@…` job AT ALL.
//
// Three worker shapes, all of which must come back with only the cloud job:
//   - a worker in a real cloud region (prefix matching applies);
//   - a worker whose region is a string prefix of the private slug, which is
//     the case a prefix-matching bug would leak through;
//   - a worker with NO region at all, which used to drop the region predicate
//     entirely and claim everything in the queue.
//
//nolint:paralleltest // shares database state, like its siblings in this package
func TestCloudLaneNeverClaimsPrivateRegionJobs(t *testing.T) {
	dbSvc, ctx := setupTestDB(t)
	defer func() { _ = dbSvc.Close() }()

	svc := checkjobsvc.NewService(dbSvc.DB())
	org := createTestOrg(t, ctx, dbSvc)

	now := time.Now()
	due := now.Add(-time.Minute)

	private := "@aws-paris"
	cloud := "eu-west-1"

	privateJob := createTestCheckJob(t, ctx, dbSvc, org.UID, due, &private)
	cloudJob := createTestCheckJob(t, ctx, dbSvc, org.UID, due, &cloud)

	cases := []struct {
		name          string
		workerRegion  *string
		wantCloudSeen bool
	}{
		{name: "cloud region worker", workerRegion: &cloud, wantCloudSeen: true},
		// "aws" is a prefix of "aws-paris": if the '@' were ever stripped before
		// matching, this worker would claim the private job.
		{name: "prefix-shaped worker", workerRegion: strPtr("aws"), wantCloudSeen: false},
		{name: "region-less worker", workerRegion: nil, wantCloudSeen: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { //nolint:paralleltest // shares database state
			r := require.New(t)

			worker := createTestWorker(t, ctx, dbSvc, tc.workerRegion)

			claimed, _, err := svc.ClaimJobs(ctx, worker.UID, tc.workerRegion, 10, 10, 5*time.Minute)
			r.NoError(err)

			for _, job := range claimed {
				r.NotEqual(privateJob.UID, job.UID,
					"the cloud claim lane must never claim a private-region job")
				r.NotNil(job.Region)
				r.NotEqual(private, *job.Region)
			}

			if !tc.wantCloudSeen {
				return
			}

			// Positive control: this worker DOES claim the cloud job, so an
			// empty result above cannot be mistaken for "nothing was due".
			sawCloud := false

			for _, job := range claimed {
				if job.UID == cloudJob.UID {
					sawCloud = true
				}
			}

			r.True(sawCloud, "the cloud job must still be claimable by this worker")

			// Release it so the next sub-case starts from the same queue state.
			r.NoError(svc.ReleaseLease(ctx, cloudJob.UID, worker.UID, due))
		})
	}
}

func strPtr(s string) *string {
	return &s
}
