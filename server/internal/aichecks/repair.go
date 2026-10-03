package aichecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/ai"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/checkversion"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/db/dbctx"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
)

// Repair outcomes, recorded on the attempt event.
const (
	OutcomeSkipped     = "skipped"
	OutcomeNoCandidate = "no_candidate"
	OutcomeRejected    = "rejected"
	OutcomeProposed    = "proposed"
	OutcomeApplied     = "applied"
)

// RepairOutcome is what one repair attempt did.
type RepairOutcome struct {
	Outcome string
	// Reason is the one-line why: the refused gate, the failed guard, or the
	// summary stored on the version.
	Reason string
	// Version is the proposed or applied version, 0 for none.
	Version int
}

// Mailer queues an email. The jobs service satisfies it through
// JobsMailer.
type Mailer interface {
	Mail(ctx context.Context, orgUID string, to []string, subject, text string) error
}

// JobsMailer sends mail through the email job.
type JobsMailer struct {
	Jobs jobsvc.Service
}

// Mail implements Mailer.
func (m *JobsMailer) Mail(ctx context.Context, orgUID string, to []string, subject, text string) error {
	raw, err := json.Marshal(map[string]any{"to": to, "subject": subject, "text": text})
	if err != nil {
		return err
	}

	_, err = m.Jobs.CreateJob(ctx, orgUID, "email", raw, nil)

	return err
}

// SetMailer sets how an applied auto-repair notifies the check's owner.
func (s *Service) SetMailer(mailer Mailer) {
	s.mailer = mailer
}

// SetBaseURL sets the dashboard base URL used in notification links.
func (s *Service) SetBaseURL(baseURL string) {
	s.baseURL = strings.TrimRight(baseURL, "/")
}

// repairTarget is everything a repair works from.
type repairTarget struct {
	check   *models.Check
	org     *models.Organization
	cfg     *checkjs.JSConfig
	secrets map[string]string
	latest  *models.Result
}

// RepairCheck evaluates the repair gates of a check and, when they all hold,
// runs one repair attempt. It is the ai_repair job's body. A gate refusing is
// not an error: it is the common case.
func (s *Service) RepairCheck(ctx context.Context, orgUID, checkUID string) error {
	_, err := s.Repair(ctx, orgUID, checkUID)

	return err
}

// Repair is RepairCheck returning what happened.
func (s *Service) Repair(ctx context.Context, orgUID, checkUID string) (*RepairOutcome, error) {
	if !s.Enabled() {
		return &RepairOutcome{Outcome: OutcomeSkipped, Reason: ErrDisabled.Error()}, nil
	}

	target, skip, err := s.loadRepairTarget(ctx, orgUID, checkUID)
	if err != nil || skip != "" {
		return &RepairOutcome{Outcome: OutcomeSkipped, Reason: skip}, err
	}

	gates, err := s.repairGates(ctx, target)
	if err != nil {
		return nil, err
	}

	if ok, reason := ShouldRepair(gates); !ok {
		return &RepairOutcome{Outcome: OutcomeSkipped, Reason: reason}, nil
	}

	if err := s.budgetLeft(ctx, orgUID); err != nil {
		return &RepairOutcome{Outcome: OutcomeSkipped, Reason: err.Error()}, nil //nolint:nilerr // a spent budget is a skip
	}

	ctx = ai.WithCallMeta(ctx, ai.CallMeta{OrgUID: orgUID, CheckUID: checkUID, Purpose: ai.PurposeRepair})

	outcome, usage, err := s.attemptRepair(ctx, target)
	s.recordAttempt(ctx, target.check, outcome, usage, err)

	return outcome, err
}

func (s *Service) loadRepairTarget(ctx context.Context, orgUID, checkUID string) (*repairTarget, string, error) {
	check, err := s.db.GetCheck(ctx, orgUID, checkUID)
	if err != nil || check == nil {
		return nil, "check not found", nil //nolint:nilerr // a deleted check is a skip
	}

	if check.Type != "js" || !check.Enabled || check.DeletedAt != nil {
		return nil, "not an enabled js check", nil
	}

	cfg := &checkjs.JSConfig{}
	if err := cfg.FromMap(check.Config); err != nil || cfg.AI == nil {
		return nil, "not an AI-authored check", nil //nolint:nilerr // a bad config is a skip
	}

	if cfg.AI.EffectiveRepair() == jsconfig.RepairOff {
		return nil, ReasonRepairOff, nil
	}

	org, err := s.db.GetOrganization(ctx, orgUID)
	if err != nil {
		return nil, "", fmt.Errorf("loading the organization: %w", err)
	}

	secrets, err := s.checkSecrets(ctx, check)
	if err != nil {
		return nil, "", err
	}

	return &repairTarget{check: check, org: org, cfg: cfg, secrets: secrets}, "", nil
}

// checkSecrets opens the check's secrets envelope. They run scripts and are
// scrubbed from everything the model sees.
func (s *Service) checkSecrets(ctx context.Context, check *models.Check) (map[string]string, error) {
	private, outcome, err := checkjobsvc.OpenSecretsEnvelope(ctx, s.creds, check.OrganizationUID, check.ConfigPrivate)
	if err != nil {
		return nil, fmt.Errorf("opening the check's secrets: %w", err)
	}

	merged := map[string]any(check.Config)
	if outcome == checkjobsvc.SecretMergeMerged {
		merged = map[string]any(models.JSONMap(private))
	}

	out := map[string]string{}

	raw, _ := merged["secrets"].(map[string]any)
	for key, value := range raw {
		if str, ok := value.(string); ok {
			out[key] = str
		}
	}

	return out, nil
}

func (s *Service) repairGates(ctx context.Context, target *repairTarget) (Gates, error) {
	now := s.now()
	gates := Gates{Mode: target.cfg.AI.EffectiveRepair(), Now: now, Threshold: DefaultConsecutiveFailures}

	results, err := s.db.ListResults(ctx, &models.ListResultsFilter{
		OrganizationUID: target.check.OrganizationUID,
		CheckUIDs:       []string{target.check.UID},
		PeriodTypes:     []string{string(models.PeriodTypeRaw)},
		Limit:           gates.Threshold,
	})
	if err != nil {
		return gates, fmt.Errorf("reading recent results: %w", err)
	}

	gates.Class = FailureNone

	for i, result := range results.Results {
		if result.Status == nil {
			break
		}

		class := ClassifyFailure(models.ResultStatus(*result.Status), result.Output)
		if class == FailureNone {
			break
		}

		if i == 0 {
			gates.Class = class
			target.latest = result
		}

		gates.ConsecutiveFailures++
	}

	if gates.Class != FailureDrift {
		return gates, nil
	}

	if err := s.attemptCounters(ctx, target, &gates); err != nil {
		return gates, err
	}

	gates.TargetHealthy = s.targetHealthy(ctx, target.cfg)

	return gates, nil
}

func (s *Service) attemptCounters(ctx context.Context, target *repairTarget, gates *Gates) error {
	checkUID := target.check.UID
	since := gates.Now.Add(-AttemptCooldown)

	recent, err := s.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: target.check.OrganizationUID,
		CheckUID:        &checkUID,
		EventTypes:      []models.EventType{models.EventTypeCheckAIRepairAttempted},
		Since:           &since,
		Limit:           1,
	})
	if err != nil {
		return fmt.Errorf("reading repair attempts: %w", err)
	}

	if len(recent) > 0 {
		gates.LastAttempt = recent[0].CreatedAt
	}

	today := dayStart(gates.Now)

	orgAttempts, err := s.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: target.check.OrganizationUID,
		EventTypes:      []models.EventType{models.EventTypeCheckAIRepairAttempted},
		Since:           &today,
		Limit:           DefaultOrgDailyAttempts + 1,
	})
	if err != nil {
		return fmt.Errorf("reading repair attempts: %w", err)
	}

	gates.OrgAttemptsToday = len(orgAttempts)

	return nil
}

// baseURL is the scheme and host of the first URL the script contacts.
func baseURL(script string, env map[string]string) string {
	code, _ := scanScript(script)

	candidates := urlLiteralRE.FindAllString(code, -1)
	for _, match := range envRefRE.FindAllStringSubmatch(code, -1) {
		name := match[1]
		if name == "" {
			name = match[2]
		}

		candidates = append(candidates, urlLiteralRE.FindAllString(env[name], -1)...)
	}

	for _, raw := range candidates {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			continue
		}

		scheme := parsed.Scheme
		switch scheme {
		case "ws":
			scheme = "http"
		case "wss":
			scheme = "https"
		}

		return scheme + "://" + parsed.Host + "/"
	}

	return ""
}

// targetHealthy reports whether the script's base URL answers 2xx/3xx: the
// service is up, so a failing script is the script's fault.
func (s *Service) targetHealthy(ctx context.Context, cfg *checkjs.JSConfig) bool {
	base := baseURL(cfg.Script, cfg.Env)
	if base == "" {
		return false
	}

	res, err := s.runner.FetchPage(ctx, base)
	if err != nil || !res.Up() {
		return false
	}

	code := payloadInt(res.Output, "statusCode")

	return code >= 200 && code < 400
}

func (s *Service) recordAttempt(
	ctx context.Context, check *models.Check, outcome *RepairOutcome, usage ai.Usage, attemptErr error,
) {
	event := models.NewEvent(check.OrganizationUID, models.EventTypeCheckAIRepairAttempted, models.ActorTypeSystem)
	checkUID := check.UID
	event.CheckUID = &checkUID
	event.Payload["inputTokens"] = usage.InputTokens
	event.Payload["outputTokens"] = usage.OutputTokens

	if outcome != nil {
		event.Payload["outcome"] = outcome.Outcome
		event.Payload["reason"] = outcome.Reason

		if outcome.Version > 0 {
			event.Payload["version"] = outcome.Version
		}
	}

	if attemptErr != nil {
		event.Payload["outcome"] = "error"
		event.Payload["error"] = attemptErr.Error()
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		s.logger.WarnContext(ctx, "Failed to record the repair attempt", "error", err, "check_uid", check.UID)
	}
}

// attemptRepair runs the repair loop, the guards and the verification run,
// then stores the candidate.
func (s *Service) attemptRepair(ctx context.Context, target *repairTarget) (*RepairOutcome, ai.Usage, error) {
	previous := target.cfg.Script
	env := target.cfg.Env

	rec := &runRecorder{
		runner: s.runner,
		env:    env,
		prepare: func(script string) (map[string]string, string, error) {
			// No candidate ever runs with the secrets against a host the
			// previous version did not contact.
			if err := CheckHosts(previous, script, env); err != nil {
				return nil, "", fmt.Errorf("refused: %w", err)
			}

			return target.secrets, "", nil
		},
	}

	tools := []ai.Tool{rec.tool(), s.runner.fetchPageTool(), s.runner.browserSnapshotTool()}
	result, loopErr := s.loop(systemPrompt(), tools, rec.done).Run(ctx, []ai.Message{
		{Role: ai.RoleUser, Content: s.repairBrief(ctx, target)},
	})

	usage := ai.Usage{}
	if result != nil {
		usage = result.Usage
	}

	if rec.lastUp == nil {
		reason := "no candidate passed its test run"
		if loopErr != nil {
			reason += ": " + loopErr.Error()
		}

		return &RepairOutcome{Outcome: OutcomeNoCandidate, Reason: reason}, usage, nil
	}

	outcome, err := s.storeCandidate(ctx, target, rec.lastUp.Script)

	return outcome, usage, err
}

// storeCandidate applies the guards and the verification run, then stores
// the candidate as a proposal, or applies it with `repair: auto`.
func (s *Service) storeCandidate(ctx context.Context, target *repairTarget, candidate string) (*RepairOutcome, error) {
	mode := target.cfg.AI.EffectiveRepair()
	cfg := target.cfg

	guardErr := CheckCandidate(cfg.Script, candidate, cfg.Env)
	if guardErr == nil {
		verify, err := s.runner.Run(ctx, candidate, cfg.Env, target.secrets, cfg.Timeout)
		if err != nil {
			guardErr = err
		} else if !verify.Up() {
			guardErr = fmt.Errorf("the verification run returned %s", verify.Status)
		}
	}

	if guardErr != nil && mode != jsconfig.RepairAuto {
		return &RepairOutcome{Outcome: OutcomeRejected, Reason: guardErr.Error()}, nil
	}

	reason := repairReason(target.latest)
	if guardErr != nil {
		reason += " (not applied: " + guardErr.Error() + ")"
	}

	if guardErr == nil && mode == jsconfig.RepairAuto {
		recent, err := s.appliedRepairWithin(ctx, target.check.UID, AutoApplyCooldown)
		if err != nil {
			return nil, err
		}

		if !recent {
			return s.applyRepair(ctx, target, candidate, reason)
		}

		reason += " (not applied: a repair was already applied in the last 24 h)"
	}

	return s.proposeRepair(ctx, target, candidate, reason)
}

func repairReason(latest *models.Result) string {
	msg := "the script drifted"

	if latest != nil {
		if errMsg, ok := latest.Output[outputKeyError].(string); ok && errMsg != "" {
			msg += ": " + errMsg
		} else if step, ok := latest.Output["step"].(string); ok && step != "" {
			msg += " at step " + step
		}
	}

	if len(msg) > 200 {
		msg = msg[:200] + "..."
	}

	return "AI repair: " + msg
}

// appliedRepairWithin reports an applied ai_repair version newer than window.
func (s *Service) appliedRepairWithin(ctx context.Context, checkUID string, window time.Duration) (bool, error) {
	versions, err := s.db.ListCheckVersions(ctx, checkUID, 0)
	if err != nil {
		return false, fmt.Errorf("listing check versions: %w", err)
	}

	cutoff := s.now().Add(-window)

	for _, version := range versions {
		if version.Origin == models.CheckVersionOriginAIRepair &&
			version.Status == models.CheckVersionStatusApplied && version.CreatedAt.After(cutoff) {
			return true, nil
		}
	}

	return false, nil
}

// repairedConfig is the check's config with the candidate script and the ai
// block's model and timestamp refreshed. Secrets are not in it: they never
// change in a repair, and every write path keeps the current ones.
func (s *Service) repairedConfig(target *repairTarget, candidate string) map[string]any {
	now := s.now().UTC().Truncate(time.Second)
	block := *target.cfg.AI
	block.Model = s.client.Model
	block.GeneratedAt = &now

	out := map[string]any{}

	for key, value := range target.check.Config {
		out[key] = value
	}

	out["script"] = candidate
	out["ai"] = (&checkjs.JSConfig{Script: candidate, AI: &block}).GetConfig()["ai"]

	return out
}

func (s *Service) proposeRepair(
	ctx context.Context, target *repairTarget, candidate, reason string,
) (*RepairOutcome, error) {
	proposed := *target.check
	proposed.Config = models.JSONMap(s.repairedConfig(target, candidate))

	labels, err := s.db.GetLabelsForCheck(ctx, target.check.UID)
	if err != nil {
		return nil, fmt.Errorf("loading labels: %w", err)
	}

	snap := checkversion.Build(&proposed, checkversion.LabelMap(labels))

	stored, err := snap.ToJSONMap()
	if err != nil {
		return nil, err
	}

	row := models.NewCheckVersion(target.check.OrganizationUID, target.check.UID, stored, "")
	row.Origin = models.CheckVersionOriginAIRepair
	row.Reason = &reason

	if latest, latestErr := s.db.GetLatestAppliedCheckVersion(ctx, target.check.UID); latestErr == nil && latest != nil {
		base := latest.Version
		row.BaseVersion = &base
	}

	if err := s.db.CreateCheckVersionProposal(ctx, row); err != nil {
		return nil, fmt.Errorf("storing the repair proposal: %w", err)
	}

	event := models.NewEvent(target.check.OrganizationUID, models.EventTypeCheckAIRepairProposed, models.ActorTypeSystem)
	checkUID := target.check.UID
	event.CheckUID = &checkUID
	event.Payload["version"] = row.Version
	event.Payload["reason"] = reason

	if err := s.db.CreateEvent(ctx, event); err != nil {
		s.logger.WarnContext(ctx, "Failed to record the repair proposal", "error", err, "check_uid", checkUID)
	}

	return &RepairOutcome{Outcome: OutcomeProposed, Reason: reason, Version: row.Version}, nil
}

func (s *Service) applyRepair(
	ctx context.Context, target *repairTarget, candidate, reason string,
) (*RepairOutcome, error) {
	latest, err := s.db.GetLatestAppliedCheckVersion(ctx, target.check.UID)
	if err != nil {
		return nil, fmt.Errorf("reading the latest version: %w", err)
	}

	source := &dbctx.ChangeSource{Origin: string(models.CheckVersionOriginAIRepair), Reason: reason}
	if latest != nil {
		base := latest.Version
		source.BaseVersion = &base
	}

	config := s.repairedConfig(target, candidate)
	req := &checks.UpdateCheckRequest{Config: &config}

	if _, err := s.checks.UpdateCheck(dbctx.WithChange(ctx, source), target.org.Slug, target.check.UID, req); err != nil {
		return nil, fmt.Errorf("applying the repair: %w", err)
	}

	applied, err := s.db.GetLatestAppliedCheckVersion(ctx, target.check.UID)
	if err != nil || applied == nil {
		return nil, errors.Join(errors.New("reading the applied repair"), err)
	}

	s.notifyApplied(ctx, target, previousScriptDiff(target.cfg.Script, candidate), applied, latest)

	return &RepairOutcome{Outcome: OutcomeApplied, Reason: reason, Version: applied.Version}, nil
}

// previousScriptDiff is a compact line diff of two scripts.
func previousScriptDiff(before, after string) string {
	beforeLines := strings.Split(before, "\n")
	afterLines := strings.Split(after, "\n")
	inBefore := map[string]int{}

	for _, line := range beforeLines {
		inBefore[line]++
	}

	inAfter := map[string]int{}
	for _, line := range afterLines {
		inAfter[line]++
	}

	var builder strings.Builder

	for _, line := range beforeLines {
		if inAfter[line] == 0 {
			builder.WriteString("- " + line + "\n")
		}
	}

	for _, line := range afterLines {
		if inBefore[line] == 0 {
			builder.WriteString("+ " + line + "\n")
		}
	}

	return builder.String()
}

// notifyApplied records the applied repair with its diff and mails the
// check's owner: the author of the version it replaced.
func (s *Service) notifyApplied(
	ctx context.Context, target *repairTarget, diff string, applied, replaced *models.CheckVersion,
) {
	checkUID := target.check.UID
	event := models.NewEvent(target.check.OrganizationUID, models.EventTypeCheckAIRepairApplied, models.ActorTypeSystem)
	event.CheckUID = &checkUID
	event.Payload["version"] = applied.Version
	event.Payload["diff"] = diff

	if applied.Reason != nil {
		event.Payload["reason"] = *applied.Reason
	}

	if err := s.db.CreateEvent(ctx, event); err != nil {
		s.logger.WarnContext(ctx, "Failed to record the applied repair", "error", err, "check_uid", checkUID)
	}

	if s.mailer == nil || replaced == nil || replaced.ActorUserUID == nil {
		return
	}

	owner, err := s.db.GetUser(ctx, *replaced.ActorUserUID)
	if err != nil || owner == nil || owner.Email == "" {
		return
	}

	name := target.check.UID
	if target.check.Name != nil {
		name = *target.check.Name
	}

	link := fmt.Sprintf("%s/d/orgs/%s/checks/%s/history", s.baseURL, target.org.Slug, checkUID)
	text := fmt.Sprintf("SolidPing repaired the script of the check %q (version %d).\n\n%s\n\n"+
		"Diff:\n%s\nReview or undo it: %s\n", name, applied.Version, deref(applied.Reason), diff, link)

	if err := s.mailer.Mail(ctx, target.check.OrganizationUID, []string{owner.Email},
		"Check repaired: "+name, text); err != nil {
		s.logger.WarnContext(ctx, "Failed to mail the applied repair", "error", err, "check_uid", checkUID)
	}
}

func deref(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

// repairBrief is the repair loop's first message: the contract, the old
// script, the failure and a fresh look at the target.
func (s *Service) repairBrief(ctx context.Context, target *repairTarget) string {
	var builder strings.Builder

	builder.WriteString("A js check that used to pass is now failing because the script drifted ")
	builder.WriteString("(the target changed its structure; the service itself answers). ")
	builder.WriteString("Repair the script so it checks the same contract again. ")
	builder.WriteString("Keep every assertion, contact only the same hosts, add no try/catch.\n\n")
	builder.WriteString("Contract:\n")

	for i, item := range target.cfg.AI.Contract {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, item)
	}

	builder.WriteString("\nCurrent script:\n```js\n" + target.cfg.Script + "\n```\n")

	if target.latest != nil {
		failure := &RunResult{Output: target.latest.Output}
		scrubResult(failure, target.secrets)
		builder.WriteString("\nLast failure output:\n" + forModel(failure.Output) + "\n")
	}

	if names := secretNames(target.secrets); len(names) > 0 {
		builder.WriteString("\nsecrets available (values hidden): " + strings.Join(names, ", ") + "\n")
	}

	if base := baseURL(target.cfg.Script, target.cfg.Env); base != "" {
		if snap, err := s.runner.FetchPage(ctx, base); err == nil {
			builder.WriteString("\nfetch_page " + base + ":\n" + forModel(snap) + "\n")
		}
	}

	builder.WriteString("\nUse browser_snapshot or fetch_page to inspect pages, then run_script to test.")

	return builder.String()
}
