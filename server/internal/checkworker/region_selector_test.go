package checkworker

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db/models"
)

// regionRecordingConfig implements checkerdef.RegionSelector.
type regionRecordingConfig struct {
	region   string
	selected bool
}

func (c *regionRecordingConfig) FromMap(map[string]any) error { return nil }
func (c *regionRecordingConfig) GetConfig() map[string]any    { return map[string]any{} }
func (c *regionRecordingConfig) SelectRegion(region string) {
	c.region = region
	c.selected = true
}

// regionReadingChecker records what region its config held when Execute ran.
type regionReadingChecker struct {
	sawSelected bool
	sawRegion   string
}

func (c *regionReadingChecker) Type() checkerdef.CheckType { return "test-region-selector" }
func (c *regionReadingChecker) Validate(*checkerdef.CheckSpec) error {
	return nil
}

func (c *regionReadingChecker) Execute(_ context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, _ := config.(*regionRecordingConfig)
	if cfg != nil {
		c.sawSelected = cfg.selected
		c.sawRegion = cfg.region
	}

	return &checkerdef.Result{Status: checkerdef.StatusUp}, nil
}

// TestExecuteJob_RegionSelectorGetsResolvedRegion pins spec 2026-10-03-04: a
// config implementing RegionSelector receives the job's resolved region (the
// value the result row records, here the worker's own fallback) before Execute.
//
//nolint:paralleltest // Test uses shared database state
func TestExecuteJob_RegionSelectorGetsResolvedRegion(t *testing.T) {
	runner, dbSvc, ctx := setupTestRunner(t)
	defer func() { _ = dbSvc.Close() }()

	org := models.NewOrganization("test-org", "")
	require.NoError(t, dbSvc.CreateOrganization(ctx, org))

	workerRegion := "eu-west-test"
	worker := models.NewWorker("test-worker", "Test Worker")
	worker.Region = &workerRegion
	_, err := dbSvc.DB().NewInsert().Model(worker).Exec(ctx)
	require.NoError(t, err)
	runner.setWorker(worker)

	const checkType = checkerdef.CheckType("test-region-selector")

	cfg := &regionRecordingConfig{}
	checker := &regionReadingChecker{}

	runner.getChecker = func(ct checkerdef.CheckType) (checkerdef.Checker, bool) {
		if ct == checkType {
			return checker, true
		}

		return registry.GetChecker(ct)
	}
	runner.parseConfig = func(ct checkerdef.CheckType) (checkerdef.Config, bool) {
		if ct == checkType {
			return cfg, true
		}

		return registry.ParseConfig(ct)
	}

	check := models.NewCheck(org.UID, "region-selector-"+uuid.New().String()[:8], string(checkType))
	require.NoError(t, dbSvc.CreateCheck(ctx, check))

	checkJob := new(models.CheckJob)
	require.NoError(t, dbSvc.DB().NewSelect().Model(checkJob).Where("check_uid = ?", check.UID).Scan(ctx))
	claimJobForTest(t, dbSvc, ctx, checkJob, worker.UID)

	require.NoError(t, runner.executeJob(ctx, runner.logger, checkJob))

	require.True(t, checker.sawSelected, "SelectRegion must run before Execute")
	require.Equal(t, workerRegion, checker.sawRegion)

	// An explicit job region wins over the worker's.
	jobRegion := "us-east-test"
	checkJob.Region = &jobRegion
	*cfg = regionRecordingConfig{}
	runner.selectRegion(cfg, checkJob)
	require.Equal(t, jobRegion, cfg.region)
}
