package feedback

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/files/signedurl"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage"
	"github.com/fclairamb/solidping/server/internal/version"
)

// Errors returned by the service.
var (
	ErrOrganizationNotFound = errors.New("organization not found")
	ErrURLRequired          = errors.New("url is required")
	// ErrGitHubBadStatus indicates a non-2xx response from the GitHub Issues API.
	// The error string includes the status code and body preview for diagnostics.
	ErrGitHubBadStatus = errors.New("github returned non-2xx status")
	// ErrStorageQuotaExceeded is returned when storing the incoming screenshot
	// would push the organization's live feedback-file bytes
	// (filestorage.GroupTypeReports) over config.AppConfig.FeedbackMaxStorageBytes
	// (spec 2026-09-25-26). The anonymous POST /api/mgmt/report endpoint has no
	// auth and no other cost signal, so this is what bounds how much
	// local/S3 storage a single org's inbox can be made to fill.
	ErrStorageQuotaExceeded = errors.New("feedback storage quota exceeded")
)

// maxIssueDispatchesPerHour caps GitHub issue creation across ALL orgs on this
// process (spec 2026-09-25-26). The route-level rate limit and the per-org
// storage quota bound how many reports can be filed; this additionally bounds
// how many of those turn into a call against the wired repo token, so a burst
// of legitimate-looking reports (each under both limits, from many IPs/orgs)
// still can't spam the repo's issue tracker.
const maxIssueDispatchesPerHour = 20

// SubmitReportRequest carries all the inputs from the multipart form.
// Screenshot is optional — text-only reports are valid.
type SubmitReportRequest struct {
	URL            string
	Comment        string
	OrgSlug        string
	Annotations    string
	Context        ContextPayload
	UserUID        string
	UserEmail      string
	Screenshot     io.Reader
	ScreenshotSize int64
	ScreenshotName string
	ScreenshotMIME string
}

// SubmitReportResponse is what the handler returns.
type SubmitReportResponse struct {
	UID string `json:"uid"`
}

// GitHubIssuePoster is the minimal surface we need from "something that posts
// a GitHub issue". The default impl uses net/http; tests inject a stub.
type GitHubIssuePoster interface {
	CreateIssue(ctx context.Context, repo, token, title, body string, labels []string) error
}

// Service implements the bug-report flow.
type Service struct {
	db     db.Service
	files  *files.Service
	cfg    *config.Config
	github GitHubIssuePoster
	logger *slog.Logger
	clock  func() time.Time

	// dispatchMu guards the in-process, per-hour GitHub-issue-dispatch
	// counter below. Process-local by design: it needs no persistence (an
	// operator restart resetting the window is fine — it exists to smooth a
	// burst, not to be a hard external quota) and no cross-instance
	// coordination (each instance's own dispatch load is independent).
	dispatchMu          sync.Mutex
	dispatchWindowStart time.Time
	dispatchCount       int
}

// NewService constructs a feedback service. github may be nil — when so, a
// default HTTP-backed poster is used.
func NewService(
	dbService db.Service, filesService *files.Service, cfg *config.Config, github GitHubIssuePoster,
) *Service {
	if github == nil {
		github = &httpGitHubPoster{client: &http.Client{Timeout: 30 * time.Second}}
	}

	return &Service{
		db:     dbService,
		files:  filesService,
		cfg:    cfg,
		github: github,
		logger: slog.Default(),
		clock:  time.Now,
	}
}

// SubmitReport persists the screenshot (if any) as a File and returns its UID.
// GitHub issue creation is dispatched in a goroutine — the user-facing call
// returns success the moment the file is durable.
func (s *Service) SubmitReport(
	ctx context.Context, req *SubmitReportRequest,
) (*SubmitReportResponse, error) {
	if req.URL == "" {
		return nil, ErrURLRequired
	}

	orgSlug := req.OrgSlug
	if orgSlug == "" {
		// Fallback to the first non-deleted org. Used by status0 (subscriber-side)
		// where the user has no org context.
		orgs, err := s.db.ListOrganizations(ctx)
		if err != nil {
			return nil, fmt.Errorf("list organizations: %w", err)
		}

		if len(orgs) == 0 {
			return nil, ErrOrganizationNotFound
		}

		orgSlug = orgs[0].Slug
	}

	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	orgUUID, err := uuid.Parse(org.UID)
	if err != nil {
		return nil, fmt.Errorf("parse org uid: %w", err)
	}

	var fileUID, fileURI, fileMIME string

	if req.Screenshot != nil && req.ScreenshotSize > 0 {
		if err := s.checkStorageQuota(ctx, org.UID, req.ScreenshotSize); err != nil {
			return nil, err
		}

		var createdBy *string
		if req.UserUID != "" {
			uid := req.UserUID
			createdBy = &uid
		}

		file, ferr := s.files.CreateFile(
			ctx, orgUUID, filestorage.GroupTypeReports,
			req.ScreenshotName, req.ScreenshotMIME, createdBy,
			req.Screenshot, req.ScreenshotSize,
		)
		if ferr != nil {
			return nil, fmt.Errorf("create file: %w", ferr)
		}

		fileUID = file.UID
		fileURI = file.FileURI
		fileMIME = file.MimeType
	}

	if fileUID == "" {
		// No screenshot: still return a stable UID so the frontend can show
		// "report received". Generate ad-hoc; never persisted.
		fileUID = uuid.NewString()
	}

	if s.cfg.App.EnableBugReport {
		// Detached context — the HTTP request is already done. We log the
		// goroutine err separately rather than propagating to the caller.
		go s.dispatchGitHubIssue(context.WithoutCancel(ctx), req, org.Slug, fileUID, fileURI, fileMIME)
	}

	return &SubmitReportResponse{UID: fileUID}, nil
}

// checkStorageQuota returns ErrStorageQuotaExceeded when persisting `incoming`
// more bytes of feedback-report screenshot for orgUID would exceed
// cfg.App.FeedbackMaxStorageBytes. A quota of 0 or negative disables the
// check (config.AppConfig.FeedbackMaxStorageBytes doc comment). Any other
// error is a genuine failure to read the current usage and is propagated
// as-is, so the caller (SubmitReport) reports it as an internal error rather
// than a false 413.
func (s *Service) checkStorageQuota(ctx context.Context, orgUID string, incoming int64) error {
	quota := s.cfg.App.FeedbackMaxStorageBytes
	if quota <= 0 {
		return nil
	}

	used, err := s.db.SumFileSizeByGroup(ctx, orgUID, string(filestorage.GroupTypeReports))
	if err != nil {
		return fmt.Errorf("sum feedback storage: %w", err)
	}

	if used+incoming > quota {
		s.logger.WarnContext(ctx, "bug_report: storage quota exceeded",
			"org_uid", orgUID,
			"used_bytes", used,
			"incoming_bytes", incoming,
			"quota_bytes", quota,
		)

		return ErrStorageQuotaExceeded
	}

	return nil
}

// allowIssueDispatch enforces maxIssueDispatchesPerHour across all orgs on
// this process, using a fixed hourly window measured from the first dispatch
// in it (not a rolling one) — simple and sufficient for smoothing a burst,
// which is all this exists to do.
func (s *Service) allowIssueDispatch() bool {
	s.dispatchMu.Lock()
	defer s.dispatchMu.Unlock()

	now := s.clock()
	if s.dispatchWindowStart.IsZero() || now.Sub(s.dispatchWindowStart) >= time.Hour {
		s.dispatchWindowStart = now
		s.dispatchCount = 0
	}

	if s.dispatchCount >= maxIssueDispatchesPerHour {
		return false
	}

	s.dispatchCount++

	return true
}

// dispatchGitHubIssue is the async side. Logs failures but never propagates
// to the caller — the report is already saved.
func (s *Service) dispatchGitHubIssue(
	parent context.Context, req *SubmitReportRequest, orgSlug, fileUID, fileURI, fileMIME string,
) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	defer cancel()

	github := s.cfg.App.GitHub
	if github.IssuesToken == "" || github.Repo == "" {
		s.logger.WarnContext(ctx, "bug_report: github not configured")

		return
	}

	if !s.allowIssueDispatch() {
		s.logger.WarnContext(ctx, "bug_report: github issue dispatch capped",
			"file_uid", fileUID,
			"limit_per_hour", maxIssueDispatchesPerHour,
		)

		return
	}

	signedURL, exp := s.signedScreenshotURL(req.URL, fileUID, fileURI)

	input := &IssueInput{
		URL:           req.URL,
		Comment:       req.Comment,
		OrgSlug:       orgSlug,
		UserEmail:     req.UserEmail,
		ServerVersion: version.Version,
		GitHash:       version.Commit,
		FrontendBuild: req.Context.Build,
		Context:       req.Context,
		FileUID:       fileUID,
		SignedURL:     signedURL,
		SignedURLExp:  exp,
		MimeType:      fileMIME,
		ReportedAt:    s.clock(),
	}

	title := BuildIssueTitle(input)
	body := BuildIssueBody(input)

	if err := s.github.CreateIssue(
		ctx, github.Repo, github.IssuesToken, title, body, []string{"in-app-report"},
	); err != nil {
		s.logger.WarnContext(ctx, "bug_report: github create issue failed",
			"err", err.Error(),
			"file_uid", fileUID,
		)

		return
	}

	s.logger.InfoContext(ctx, "bug_report: issue created", "file_uid", fileUID)
}

// signedScreenshotURL returns the absolute URL the issue body should embed,
// plus its expiry. baseURL preference: the host of the page the user was on
// (so links work for the right tenant), with cfg.Server.BaseURL as fallback.
func (s *Service) signedScreenshotURL(reportURL, fileUID, fileURI string) (string, time.Time) {
	if fileUID == "" || fileURI == "" {
		return "", time.Time{}
	}

	parsedFileUID, err := uuid.Parse(fileUID)
	if err != nil {
		return "", time.Time{}
	}

	expSec, sig := signedurl.Sign([]byte(s.cfg.Auth.JWTSecret), parsedFileUID, signedurl.MaxSignedURLTTL)
	exp := time.Unix(expSec, 0)

	return signedurl.BuildURL(s.baseURLFor(reportURL), parsedFileUID, expSec, sig), exp
}

func (s *Service) baseURLFor(reportURL string) string {
	if reportURL != "" {
		if u, err := url.Parse(reportURL); err == nil && u.Scheme != "" && u.Host != "" {
			return u.Scheme + "://" + u.Host
		}
	}

	return s.cfg.Server.BaseURL
}

// httpGitHubPoster is the production implementation of GitHubIssuePoster.
type httpGitHubPoster struct {
	client             *http.Client
	overrideAPIBaseURL string // for tests
}

func (p *httpGitHubPoster) CreateIssue(
	ctx context.Context, repo, token, title, body string, labels []string,
) error {
	apiBase := p.overrideAPIBaseURL
	if apiBase == "" {
		apiBase = "https://api.github.com"
	}

	payload := map[string]any{
		"title":  title,
		"body":   body,
		"labels": labels,
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}

	endpoint := fmt.Sprintf("%s/repos/%s/issues", apiBase, repo)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}

	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("X-Github-Api-Version", "2022-11-28")
	httpReq.Header.Set("Accept", "application/vnd.github+json")
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("post issue: %w", err)
	}

	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		preview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))

		return fmt.Errorf("%w: %d: %s", ErrGitHubBadStatus, resp.StatusCode, string(preview))
	}

	return nil
}
