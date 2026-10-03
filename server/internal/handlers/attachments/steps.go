package attachments

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// MaxStepStateBytes caps one multi-step state file (spec 2026-10-03-03),
// below MaxAttachmentBytes. A slice whose next state is bigger is re-run as
// Final instead of being saved.
const MaxStepStateBytes = 1024 * 1024

// StepStateVersion is the envelope version written today.
const StepStateVersion = 1

// Details keys of a step-state file.
const (
	DetailKeyRunUID   = "runUid"
	DetailKeyStep     = "step"
	DetailKeyProgress = "progress"
)

// Errors of the multi-step state rail.
var (
	errNotEnvelope     = errors.New("step-state must be a {v, runUid, checkType, payload} envelope")
	errNotCrawlReport  = errors.New("crawl-report must be a JSON object with a findings array")
	ErrStepFileMissing = errors.New("step file not found")
)

// StepStateEnvelope wraps a checker's opaque state with what is needed to
// tell it apart from another run's: the run uid and the check type.
type StepStateEnvelope struct {
	V         int             `json:"v"`
	RunUID    string          `json:"runUid"`
	CheckType string          `json:"checkType"`
	Payload   json.RawMessage `json:"payload"`
}

// EncodeStepState wraps payload (JSON) in a version-1 envelope.
func EncodeStepState(runUID, checkType string, payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		payload = []byte("null")
	}

	return json.Marshal(StepStateEnvelope{
		V: StepStateVersion, RunUID: runUID, CheckType: checkType, Payload: payload,
	})
}

// DecodeStepState parses and checks an envelope.
func DecodeStepState(body []byte) (*StepStateEnvelope, error) {
	var env StepStateEnvelope

	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&env); err != nil {
		return nil, errNotEnvelope
	}

	if env.V != StepStateVersion || env.RunUID == "" || env.CheckType == "" || len(env.Payload) == 0 {
		return nil, errNotEnvelope
	}

	return &env, nil
}

func validateStepStateEnvelope(body []byte) error {
	_, err := DecodeStepState(body)

	return err
}

func validateCrawlReport(body []byte) error {
	var report struct {
		Findings []json.RawMessage `json:"findings"`
	}

	if err := json.Unmarshal(body, &report); err != nil || report.Findings == nil {
		return errNotCrawlReport
	}

	return nil
}

// PutStepState stores one multi-step state file under
// `checks/<uid>/step-state` (append, keep 2) and returns its file uid. The
// run uid, step number and progress go in the file's details bag, which is
// what GET .../checks/:check/run reads.
func (s *Service) PutStepState(
	ctx context.Context, orgUID, checkUID string, envelope []byte, details models.JSONMap,
) (string, error) {
	if len(envelope) > MaxStepStateBytes {
		return "", ErrAttachmentTooLarge
	}

	return s.Put(ctx, orgUID, CheckStepStateTopic(checkUID), "check-"+checkUID+"-step-state", envelope, details)
}

// PutCrawlReport stores a finished crawl's report under
// `checks/<uid>/crawl-report` (append, keep 5).
func (s *Service) PutCrawlReport(
	ctx context.Context, orgUID, checkUID string, report []byte, details models.JSONMap,
) (string, error) {
	return s.Put(ctx, orgUID, CheckCrawlReportTopic(checkUID), "check-"+checkUID+"-crawl-report", report, details)
}

// ReadCheckFile returns the bytes and row of one live file of the org that
// belongs to checkUID under the given kind. Anything else is
// ErrStepFileMissing: a pointer can never be used to read another check's or
// another org's file.
func (s *Service) ReadCheckFile(
	ctx context.Context, orgUID, checkUID, kind, fileUID string,
) ([]byte, *models.File, error) {
	if s.files == nil {
		return nil, nil, errNoFiles
	}

	file, err := s.files.GetFileByUID(ctx, fileUID)
	if err != nil || file == nil || file.OrganizationUID != orgUID ||
		file.Topic == nil || *file.Topic != CheckTopicPrefix(checkUID)+kind {
		return nil, nil, ErrStepFileMissing
	}

	reader, err := s.files.OpenContent(ctx, file)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", kind, err)
	}

	defer func() { _ = reader.Close() }()

	body, err := io.ReadAll(io.LimitReader(reader, MaxAttachmentBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read %s: %w", kind, err)
	}

	return body, file, nil
}

// LatestCrawlReport returns the newest stored crawl report of a check, or
// nil when there is none.
func (s *Service) LatestCrawlReport(ctx context.Context, orgUID, checkUID string) ([]byte, error) {
	if s.files == nil {
		return nil, errNoFiles
	}

	rows, err := s.files.ListAttachments(ctx, orgUID, CheckCrawlReportTopic(checkUID))
	if err != nil || len(rows) == 0 {
		return nil, err
	}

	body, _, err := s.ReadCheckFile(ctx, orgUID, checkUID, KindCrawlReport, rows[0].UID)

	return body, err
}

// ListCrawlReports returns a check's stored crawl reports, newest first, with
// signed download URLs.
func (s *Service) ListCrawlReports(ctx context.Context, orgUID, checkUID string) ([]Response, error) {
	if s.files == nil {
		return nil, errNoFiles
	}

	rows, err := s.files.ListAttachments(ctx, orgUID, CheckCrawlReportTopic(checkUID))
	if err != nil {
		return nil, err
	}

	out := make([]Response, 0, len(rows))
	for _, row := range rows {
		out = append(out, s.toResponse(row))
	}

	return out, nil
}

// PurgeStepState removes every state file of a check (run canceled).
func (s *Service) PurgeStepState(ctx context.Context, orgUID, checkUID string) error {
	if s.files == nil {
		return errNoFiles
	}

	rows, err := s.files.ListAttachments(ctx, orgUID, CheckStepStateTopic(checkUID))
	if err != nil {
		return err
	}

	for _, row := range rows {
		if purgeErr := s.files.PurgeFile(ctx, row); purgeErr != nil {
			return purgeErr
		}
	}

	return nil
}

// StepStateDetails builds the details bag of a state file.
func StepStateDetails(runUID string, step int, progress map[string]any) models.JSONMap {
	details := models.JSONMap{
		DetailKeyRunUID:     runUID,
		DetailKeyStep:       step,
		DetailKeyCapturedAt: time.Now().UTC().Format(time.RFC3339),
	}

	if progress != nil {
		details[DetailKeyProgress] = progress
	}

	return details
}
