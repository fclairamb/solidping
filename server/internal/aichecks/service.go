package aichecks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fclairamb/solidping/server/internal/ai"
	jsconfig "github.com/fclairamb/solidping/server/internal/checkers/checkjs/config"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// Service errors.
var (
	// ErrDisabled means no AI provider is configured.
	ErrDisabled = errors.New("AI-authored checks are not enabled on this server")
	// ErrBudgetExceeded means the org spent its daily AI token budget.
	ErrBudgetExceeded = errors.New("the organization's daily AI token budget is spent")
	// ErrPromptRequired is a generation without a prompt.
	ErrPromptRequired = errors.New("prompt is required")
	// ErrContractRequired is a generation without a confirmed contract.
	ErrContractRequired = errors.New("contract is required")
	// ErrNoContract is a model answer without a usable contract.
	ErrNoContract = errors.New("the model did not return a contract")
	// ErrNoPassingScript is a loop that ended without a passing script.
	ErrNoPassingScript = errors.New("no script passed its test run")
)

// enabled mirrors whether a provider is configured, for the hot result path
// that decides whether to queue a repair job.
var enabled atomic.Bool //nolint:gochecknoglobals // process-wide feature switch

// Enabled reports whether a provider is configured on this process.
func Enabled() bool {
	return enabled.Load()
}

// SetEnabled flips the process-wide switch. Called once at startup.
func SetEnabled(on bool) {
	enabled.Store(on)
}

// Service authors and repairs AI-written js checks.
type Service struct {
	client       *ai.Client
	db           db.Service
	checks       *checks.Service
	entitlements *entitlements.Service
	creds        credentials.Service
	runner       *Runner
	logger       *slog.Logger
	now          func() time.Time
	mailer       Mailer
	baseURL      string
}

// Options are the service's dependencies.
type Options struct {
	Client       *ai.Client
	DB           db.Service
	Checks       *checks.Service
	Entitlements *entitlements.Service
	Credentials  credentials.Service
	Runner       *Runner
	Logger       *slog.Logger
	Now          func() time.Time
}

// NewService builds the service. A nil Client is the feature off.
func NewService(opts Options) *Service {
	svc := &Service{
		client:       opts.Client,
		db:           opts.DB,
		checks:       opts.Checks,
		entitlements: opts.Entitlements,
		creds:        opts.Credentials,
		runner:       opts.Runner,
		logger:       opts.Logger,
		now:          opts.Now,
	}

	if svc.runner == nil {
		svc.runner = &Runner{}
	}

	if svc.logger == nil {
		svc.logger = slog.Default()
	}

	if svc.now == nil {
		svc.now = time.Now
	}

	return svc
}

// Enabled reports whether the service has a provider.
func (s *Service) Enabled() bool {
	return s != nil && s.client != nil
}

// Runner returns the script runner, shared with the MCP tools.
func (s *Service) Runner() *Runner {
	return s.runner
}

func dayStart(t time.Time) time.Time {
	t = t.UTC()

	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// tokensToday sums the org's AI tokens this UTC day.
func (s *Service) tokensToday(ctx context.Context, orgUID string) (int, error) {
	since := dayStart(s.now())

	events, err := s.db.ListEvents(ctx, &models.ListEventsFilter{
		OrganizationUID: orgUID,
		EventTypes:      []models.EventType{models.EventTypeAIUsage},
		Since:           &since,
		Limit:           100000,
	})
	if err != nil {
		return 0, fmt.Errorf("reading AI usage: %w", err)
	}

	total := 0
	for _, event := range events {
		total += payloadInt(event.Payload, "inputTokens") + payloadInt(event.Payload, "outputTokens")
	}

	return total, nil
}

func payloadInt(payload models.JSONMap, key string) int {
	switch value := payload[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		n, _ := value.Int64()

		return int(n)
	}

	return 0
}

// budgetLeft reports whether the org may spend more tokens today. A nil cap
// (self-hosted) is unlimited.
func (s *Service) budgetLeft(ctx context.Context, orgUID string) error {
	if s.entitlements == nil {
		return nil
	}

	resolved, err := s.entitlements.Resolve(ctx, orgUID)
	if err != nil {
		return fmt.Errorf("resolving entitlements: %w", err)
	}

	limit := resolved.Limits.MaxAITokensPerDay
	if limit == nil {
		return nil
	}

	used, err := s.tokensToday(ctx, orgUID)
	if err != nil {
		return err
	}

	if used >= *limit {
		return ErrBudgetExceeded
	}

	return nil
}

// recordUsage writes one ai.usage event. Best-effort: a failed write never
// fails the call it accounts for.
func (s *Service) recordUsage(ctx context.Context, usage ai.Usage) {
	meta := ai.CallMetaFromContext(ctx)
	if meta.OrgUID == "" {
		return
	}

	event := models.NewEvent(meta.OrgUID, models.EventTypeAIUsage, models.ActorTypeSystem)
	if meta.CheckUID != "" {
		checkUID := meta.CheckUID
		event.CheckUID = &checkUID
	}

	event.Payload["purpose"] = string(meta.Purpose)
	event.Payload["model"] = s.client.Model
	event.Payload["inputTokens"] = usage.InputTokens
	event.Payload["outputTokens"] = usage.OutputTokens

	if err := s.db.CreateEvent(ctx, event); err != nil {
		s.logger.WarnContext(ctx, "Failed to record AI usage", "error", err, "org_uid", meta.OrgUID)
	}
}

func (s *Service) loop(system string, tools []ai.Tool, done func() bool) *ai.Loop {
	return &ai.Loop{
		Client:  s.client,
		System:  system,
		Tools:   tools,
		Done:    done,
		OnUsage: s.recordUsage,
		Logger:  s.logger,
	}
}

// ContractResponse is the proposed contract.
type ContractResponse struct {
	Contract []string `json:"contract"`
	Model    string   `json:"model"`
	Usage    ai.Usage `json:"usage"`
}

// Contract turns a prompt into a contract for the user to confirm.
func (s *Service) Contract(ctx context.Context, orgUID, prompt string) (*ContractResponse, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}

	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return nil, ErrPromptRequired
	}

	if err := s.budgetLeft(ctx, orgUID); err != nil {
		return nil, err
	}

	ctx = ai.WithCallMeta(ctx, ai.CallMeta{OrgUID: orgUID, Purpose: ai.PurposeContract})

	resp, err := s.loop(contractSystemPrompt, nil, nil).Complete(ctx, ai.Request{
		System:   contractSystemPrompt,
		Messages: []ai.Message{{Role: ai.RoleUser, Content: prompt}},
	})
	if err != nil {
		return nil, err
	}

	contract := parseContract(resp.Text)
	if len(contract) == 0 {
		return nil, ErrNoContract
	}

	return &ContractResponse{Contract: contract, Model: s.client.Model, Usage: resp.Usage}, nil
}

var listItemRE = regexp.MustCompile(`^\s*(?:[-*]|\d+[.)])\s+`)

// parseContract reads a JSON array of strings, tolerating a code fence or a
// plain list.
func parseContract(text string) []string {
	if start, end := strings.Index(text, "["), strings.LastIndex(text, "]"); start >= 0 && end > start {
		var items []string
		if err := json.Unmarshal([]byte(text[start:end+1]), &items); err == nil {
			return cleanContract(items)
		}
	}

	var items []string

	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "```") {
			continue
		}

		items = append(items, listItemRE.ReplaceAllString(line, ""))
	}

	return cleanContract(items)
}

func cleanContract(items []string) []string {
	out := make([]string, 0, len(items))

	for _, item := range items {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}

	return out
}

// GenerateRequest is a generation from a confirmed contract.
type GenerateRequest struct {
	Prompt   string            `json:"prompt"`
	Contract []string          `json:"contract"`
	Env      map[string]string `json:"env,omitempty"`
	// Secrets are the values the user typed for the check. They are used to
	// run the script and never reach the model, which only sees their names.
	Secrets map[string]string    `json:"secrets,omitempty"`
	Repair  jsconfig.RepairMode `json:"repair,omitempty"`
}

// GenerateResponse is a script that passed its test run, ready to save.
type GenerateResponse struct {
	Script string `json:"script"`
	// Config is the js check config to save, without the secrets (the
	// dashboard still holds them).
	Config      map[string]any `json:"config"`
	SecretNames []string       `json:"secretNames"`
	LastRun     *RunResult     `json:"lastRun"`
	Model       string         `json:"model"`
	Turns       int            `json:"turns"`
	Usage       ai.Usage       `json:"usage"`
}

// GenerationError is a generation that produced no passing script. It
// carries the last attempt so the user can see what happened.
type GenerationError struct {
	Err        error
	LastScript string
	LastRun    *RunResult
	Turns      int
}

// Error implements error.
func (e *GenerationError) Error() string {
	return e.Err.Error()
}

// Unwrap implements errors.Unwrap.
func (e *GenerationError) Unwrap() error {
	return e.Err
}

func validateGenerate(req *GenerateRequest) error {
	req.Prompt = strings.TrimSpace(req.Prompt)
	req.Contract = cleanContract(req.Contract)

	if req.Prompt == "" {
		return ErrPromptRequired
	}

	if len(req.Contract) == 0 {
		return ErrContractRequired
	}

	block := &jsconfig.AIConfig{Prompt: req.Prompt, Contract: req.Contract, Repair: req.Repair}

	return block.Validate()
}

// Generate runs the authoring loop until a script passes run_script, or the
// turn cap is hit (an error, never a saved script).
func (s *Service) Generate(ctx context.Context, orgUID string, req *GenerateRequest) (*GenerateResponse, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}

	if err := validateGenerate(req); err != nil {
		return nil, err
	}

	if err := s.budgetLeft(ctx, orgUID); err != nil {
		return nil, err
	}

	ctx = ai.WithCallMeta(ctx, ai.CallMeta{OrgUID: orgUID, Purpose: ai.PurposeGenerate})

	declared := append([]string{req.Prompt}, req.Contract...)
	rec := &runRecorder{
		runner: s.runner,
		env:    req.Env,
		prepare: func(script string) (map[string]string, string, error) {
			// The real values only run a script whose every host the user
			// named: page content cannot talk the model into sending them
			// elsewhere.
			if hostsDeclared(script, req.Env, declared...) {
				return req.Secrets, "", nil
			}

			return blankSecrets(req.Secrets), "secrets were blank for this run: the script contacts a host " +
				"the user did not name", nil
		},
	}

	tools := []ai.Tool{rec.tool(), s.runner.fetchPageTool(), s.runner.browserSnapshotTool()}
	result, err := s.loop(systemPrompt(), tools, func() bool {
		return rec.done() || s.budgetLeft(ctx, orgUID) != nil
	}).Run(ctx, []ai.Message{{Role: ai.RoleUser, Content: generationBrief(req)}})

	if rec.lastUp == nil {
		cause := ErrNoPassingScript
		if err != nil {
			cause = fmt.Errorf("%w: %w", ErrNoPassingScript, err)
		}

		genErr := &GenerationError{Err: cause}
		if result != nil {
			genErr.Turns = result.Turns
		}

		if rec.last != nil {
			genErr.LastScript, genErr.LastRun = rec.last.Script, rec.last.Result
		}

		return nil, genErr
	}

	return s.generated(req, rec.lastUp, result), nil
}

func (s *Service) generated(req *GenerateRequest, run *scriptRun, result *ai.LoopResult) *GenerateResponse {
	now := s.now().UTC().Truncate(time.Second)
	block := &jsconfig.AIConfig{
		Prompt: req.Prompt, Contract: req.Contract, Model: s.client.Model, GeneratedAt: &now, Repair: req.Repair,
	}

	cfg := &jsconfig.JSConfig{Script: run.Script, Env: req.Env, AI: block}
	names := secretNames(req.Secrets)
	sort.Strings(names)

	out := &GenerateResponse{
		Script:      run.Script,
		Config:      cfg.GetConfig(),
		SecretNames: names,
		LastRun:     run.Result,
		Model:       s.client.Model,
	}

	if result != nil {
		out.Turns, out.Usage = result.Turns, result.Usage
	}

	return out
}

func generationBrief(req *GenerateRequest) string {
	var builder strings.Builder

	builder.WriteString("Write a js check for this request.\n\nRequest:\n")
	builder.WriteString(req.Prompt)
	builder.WriteString("\n\nContract (confirmed by the user, implement every line in order):\n")

	for i, item := range req.Contract {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, item)
	}

	if len(req.Env) > 0 {
		keys := make([]string, 0, len(req.Env))
		for key := range req.Env {
			keys = append(keys, key)
		}

		sort.Strings(keys)
		builder.WriteString("\nenv (plain parameters):\n")

		for _, key := range keys {
			fmt.Fprintf(&builder, "- env.%s = %q\n", key, req.Env[key])
		}
	}

	if names := secretNames(req.Secrets); len(names) > 0 {
		sort.Strings(names)
		builder.WriteString("\nsecrets (values hidden, use secrets.NAME): ")
		builder.WriteString(strings.Join(names, ", "))
		builder.WriteString("\n")
	}

	builder.WriteString("\nExplore the target with fetch_page or browser_snapshot, then test with run_script.")

	return builder.String()
}
