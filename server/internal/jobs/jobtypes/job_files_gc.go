package jobtypes

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// filesGCInterval is how often the storage-hygiene sweep runs. Reaping a blob
// is never urgent — nothing is broken while it lingers, only billed — so an
// hourly cadence is ample and keeps the sweep's own cost negligible.
const filesGCInterval = time.Hour

// defaultOrphanGraceSeconds is how long an attachment whose entity has already
// vanished is left alone before the sweep soft-deletes it.
//
// It exists because the two writes are not atomic. The agent upload path
// (spec 2026-08-21-01 §6) writes a blob against an incident the server named,
// and the incident pipeline writes one immediately after the incident row is
// inserted; a sweep with no grace could race either and delete evidence that
// was about to become valid. Two hours is far longer than either window and
// still bounds the storage exposure to one sweep interval's worth of garbage.
const defaultOrphanGraceSeconds = 2 * 60 * 60

// defaultPurgeAfterSeconds is how long a soft-deleted file keeps its bytes
// before the sweep drops them from the storage backend.
//
// Seven days: a soft delete is recoverable by construction (the row is still
// there and the blob is still addressable), and that window is what makes an
// accidental delete — or a reopen that dropped the wrong screenshot — a
// support ticket rather than a permanent loss.
const defaultPurgeAfterSeconds = 7 * 24 * 60 * 60

// filesGCBatch bounds one sweep's work. The sweep reschedules itself, so a
// large backlog drains over several runs instead of holding one long
// transaction or a lot of memory.
const filesGCBatch = 500

// FilesGCJobDefinition is the factory for the file-storage GC sweep.
type FilesGCJobDefinition struct{}

// Type returns the files GC job type.
func (d *FilesGCJobDefinition) Type() jobdef.JobType {
	return jobdef.JobTypeFilesGC
}

// FilesGCJobConfig configures the sweep.
type FilesGCJobConfig struct {
	// OrphanGraceSeconds overrides how long an attachment whose entity is
	// gone is left before being soft-deleted. Zero means the default.
	OrphanGraceSeconds int `json:"orphanGraceSeconds,omitempty"`
	// PurgeAfterSeconds overrides how long a soft-deleted file keeps its
	// bytes. Zero means the default.
	PurgeAfterSeconds int `json:"purgeAfterSeconds,omitempty"`
}

// CreateJobRun builds an executable instance.
func (d *FilesGCJobDefinition) CreateJobRun(config json.RawMessage) (jobdef.JobRunner, error) {
	var cfg FilesGCJobConfig
	if len(config) > 0 {
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
	}

	return &FilesGCJobRun{config: cfg}, nil
}

// FilesGCJobRun is the runtime state for one sweep.
type FilesGCJobRun struct {
	config FilesGCJobConfig
}

// Run performs both sweeps, then reschedules itself.
//
// The two passes are deliberately separate and deliberately ordered:
//
//  1. **Orphan attachments** — a `files` row whose `incidents/<uid>/…` topic
//     names an incident that no longer exists is soft-deleted. This is the
//     reaper the attachments rail needs (spec 2026-08-21-01 §7): the topic is
//     the ONLY link from a blob back to its entity, so when the entity goes
//     the blob is unreachable and nothing else will ever find it. Catching it
//     here rather than at each deletion site covers every route by which an
//     incident can disappear — a cascade, a manual purge, a retention job —
//     including ones that do not exist yet.
//  2. **Purgeable blobs** — a file soft-deleted longer ago than the retention
//     window has its bytes dropped from the storage backend and its row hard
//     deleted. Pass 1 feeds pass 2 one retention window later, which is what
//     turns "the row is hidden" into "we stopped paying for it".
//
// Bytes first, row second, always: a row whose blob is already gone is a
// dangling pointer a reader would 500 on, whereas a blob whose row is gone is
// merely paid-for garbage this same sweep re-finds. Failing that way round is
// the recoverable one.
func (r *FilesGCJobRun) Run(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	if jctx.DBService == nil {
		log.InfoContext(ctx, "Skipping files GC (database service not available)")

		return nil
	}

	if err := r.sweepOrphanAttachments(ctx, jctx); err != nil {
		return err
	}

	r.purgeDeletedFiles(ctx, jctx)

	r.rescheduleSelf(ctx, jctx)

	return nil
}

// sweepOrphanAttachments soft-deletes attachments whose incident is gone.
func (r *FilesGCJobRun) sweepOrphanAttachments(ctx context.Context, jctx *jobdef.JobContext) error {
	log := jctx.Logger

	orphans, err := jctx.DBService.ListOrphanIncidentAttachments(ctx, filesGCBatch)
	if err != nil {
		log.ErrorContext(ctx, "Failed to list orphan incident attachments", "error", err)

		return jobdef.NewRetryableError(fmt.Errorf("list orphan attachments: %w", err))
	}

	cutoff := time.Now().Add(-r.orphanGrace())
	swept := 0

	for _, file := range orphans {
		// The grace window is what keeps this from racing a write that is
		// about to become valid — see defaultOrphanGraceSeconds.
		if file.CreatedAt.After(cutoff) {
			continue
		}

		if delErr := jctx.DBService.DeleteFile(ctx, file.OrganizationUID, file.UID); delErr != nil {
			log.WarnContext(ctx, "Failed to soft-delete orphan attachment",
				"fileUid", file.UID, "topic", topicOf(file), "error", delErr)

			continue
		}

		swept++
	}

	if swept > 0 {
		log.InfoContext(ctx, "Soft-deleted orphan attachments",
			"swept", swept, "candidates", len(orphans))
	}

	return nil
}

// purgeDeletedFiles drops the bytes of long-soft-deleted files and then their
// rows. Never fails the run: storage hygiene is not worth retrying a whole
// sweep for, and the next run re-finds whatever this one missed.
func (r *FilesGCJobRun) purgeDeletedFiles(ctx context.Context, jctx *jobdef.JobContext) {
	log := jctx.Logger

	before := time.Now().Add(-r.purgeAfter())

	files, err := jctx.DBService.ListPurgeableDeletedFiles(ctx, before, filesGCBatch)
	if err != nil {
		log.WarnContext(ctx, "Failed to list purgeable deleted files", "error", err)

		return
	}

	if len(files) == 0 {
		return
	}

	storageCfg := storageConfigFromApp(jctx)
	purged := 0

	for _, file := range files {
		if !dropBlob(ctx, jctx, file, storageCfg) {
			continue
		}

		if purgeErr := jctx.DBService.PurgeFile(ctx, file.UID); purgeErr != nil {
			log.WarnContext(ctx, "Failed to purge file row after dropping its bytes",
				"fileUid", file.UID, "error", purgeErr)

			continue
		}

		purged++
	}

	if purged > 0 {
		log.InfoContext(ctx, "Purged soft-deleted files",
			"purged", purged, "candidates", len(files), "purgeAfter", r.purgeAfter().String())
	}
}

// dropBlob removes one file's bytes from whichever backend its URI names.
// Reports whether the row may now be purged.
//
// A blob that is already gone counts as success — the backends' DeleteFile is
// idempotent by contract, so a sweep resuming over a partly-swept batch
// converges instead of wedging on the first row it already handled.
func dropBlob(
	ctx context.Context, jctx *jobdef.JobContext, file *models.File, cfg *filestorage.Config,
) bool {
	log := jctx.Logger

	storage, err := filestorage.GetStorageForURI(file.FileURI, cfg)
	if err != nil {
		log.WarnContext(ctx, "Failed to resolve storage backend for file",
			"fileUid", file.UID, "uri", file.FileURI, "error", err)

		return false
	}

	orgUID, group, fileID, err := storage.ParseURI(file.FileURI)
	if err != nil {
		log.WarnContext(ctx, "Failed to parse file URI",
			"fileUid", file.UID, "uri", file.FileURI, "error", err)

		return false
	}

	if delErr := storage.DeleteFile(ctx, orgUID, group, fileID); delErr != nil {
		log.WarnContext(ctx, "Failed to delete file bytes",
			"fileUid", file.UID, "uri", file.FileURI, "error", delErr)

		return false
	}

	return true
}

// storageConfigFromApp projects the app config onto the storage factory's.
func storageConfigFromApp(jctx *jobdef.JobContext) *filestorage.Config {
	cfg := jctx.AppConfig

	return &filestorage.Config{
		Type:           cfg.FileStorage.Type,
		LocalRoot:      cfg.FileStorage.LocalRoot,
		S3Bucket:       cfg.FileStorage.S3Bucket,
		S3Region:       cfg.FileStorage.S3Region,
		S3Prefix:       cfg.FileStorage.S3Prefix,
		S3Endpoint:     cfg.FileStorage.S3Endpoint,
		S3UsePathStyle: cfg.FileStorage.S3UsePathStyle,
		S3AccessKey:    cfg.FileStorage.S3AccessKey,
		S3SecretKey:    cfg.FileStorage.S3SecretKey,
	}
}

// topicOf renders a file's topic for a log line, without dereferencing nil.
func topicOf(file *models.File) string {
	if file.Topic == nil {
		return ""
	}

	return *file.Topic
}

// orphanGrace resolves the orphan grace window from the job config.
func (r *FilesGCJobRun) orphanGrace() time.Duration {
	if r.config.OrphanGraceSeconds > 0 {
		return time.Duration(r.config.OrphanGraceSeconds) * time.Second
	}

	return time.Duration(defaultOrphanGraceSeconds) * time.Second
}

// purgeAfter resolves the blob retention window from the job config.
func (r *FilesGCJobRun) purgeAfter() time.Duration {
	if r.config.PurgeAfterSeconds > 0 {
		return time.Duration(r.config.PurgeAfterSeconds) * time.Second
	}

	return time.Duration(defaultPurgeAfterSeconds) * time.Second
}

func (r *FilesGCJobRun) rescheduleSelf(ctx context.Context, jctx *jobdef.JobContext) {
	if jctx.Services == nil || jctx.Services.Jobs == nil {
		return
	}

	scheduledAt := time.Now().Add(filesGCInterval)

	_, err := jctx.Services.Jobs.CreateJob(
		ctx, "", string(jobdef.JobTypeFilesGC), nil, &jobsvc.JobOptions{ScheduledAt: &scheduledAt},
	)
	if err != nil {
		jctx.Logger.WarnContext(ctx, "Failed to reschedule files GC", "error", err)
	}
}
