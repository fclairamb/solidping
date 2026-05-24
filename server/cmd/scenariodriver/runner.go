package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"text/template"
	"time"

	"gopkg.in/yaml.v3"
)

// ScenarioFile is the top-level YAML structure.
type ScenarioFile struct {
	Name        string        `yaml:"name"`
	Description string        `yaml:"description"`
	Timeout     time.Duration `yaml:"timeout"`
	Steps       []StepYAML    `yaml:"steps"`
	Cleanup     bool          `yaml:"cleanup"`
}

// StepYAML represents one step in a scenario file.
type StepYAML struct {
	// Common fields
	Kind string `yaml:"kind"`
	ID   string `yaml:"id"`

	// create_heartbeat_check
	Slug                      string `yaml:"slug"`
	ConfirmationPeriodSeconds int    `yaml:"confirmation_period_seconds"`
	RecoveryPeriodSeconds     int    `yaml:"recovery_period_seconds"`
	EscalationThreshold       int    `yaml:"escalation_threshold"`

	// create_webhook_channel
	URL string `yaml:"url"`

	// create_escalation_policy
	RepeatMax          int          `yaml:"repeat_max"`
	RepeatAfterSeconds *int         `yaml:"repeat_after_seconds"`
	Steps              []PolicyStep `yaml:"steps"`

	// assign_policy_to_check
	CheckID  string `yaml:"check_id"`
	PolicyID string `yaml:"policy_id"`

	// send_heartbeat
	Status string `yaml:"status"`

	// expect_webhook / assert_no_webhook
	ChannelID string         `yaml:"channel_id"`
	Timeout   time.Duration  `yaml:"timeout"`
	Assert    map[string]any `yaml:"assert"`

	// wait
	Duration time.Duration `yaml:"duration"`
}

// PolicyStep mirrors the YAML shape for one escalation policy step.
type PolicyStep struct {
	DelaySeconds int                `yaml:"delay_seconds"`
	Targets      []PolicyStepTarget `yaml:"targets"`
}

// PolicyStepTarget mirrors the YAML shape for one target.
type PolicyStepTarget struct {
	Type      string `yaml:"type"`
	ChannelID string `yaml:"channel_id"` // resolved to UID at runtime
}

// TemplateData is passed to text/template when rendering YAML.
type TemplateData struct {
	ReceiverURL string
	OrgSlug     string
	Timestamp   int64
}

// RunConfig holds all configuration for a scenario run.
type RunConfig struct {
	ServerURL     string
	Org           string
	Token         string
	ScenarioFile  string
	Listen        string
	PublicURL     string
	GlobalTimeout time.Duration
	JUnitPath     string
}

// stepResult records one step's outcome.
type stepResult struct {
	Name     string
	Duration time.Duration
	Err      error
}

// RunScenario is the main entry point for running a scenario.
func RunScenario(ctx context.Context, cfg RunConfig) error {
	// Start the webhook receiver.
	receiver, err := NewWebhookReceiver(cfg.Listen)
	if err != nil {
		return fmt.Errorf("start webhook receiver: %w", err)
	}

	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = receiver.Close(shutdownCtx)
	}()

	// Load and template-expand the scenario file.
	tplData := TemplateData{
		ReceiverURL: cfg.PublicURL,
		OrgSlug:     cfg.Org,
		Timestamp:   time.Now().Unix(),
	}

	scenario, err := loadScenario(cfg.ScenarioFile, tplData)
	if err != nil {
		return fmt.Errorf("load scenario: %w", err)
	}

	// Apply global timeout override if provided, otherwise use the scenario's own timeout.
	timeout := scenario.Timeout
	if cfg.GlobalTimeout > 0 {
		timeout = cfg.GlobalTimeout
	}

	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	client := newAPIClient(cfg.ServerURL, cfg.Token)

	runner := &scenarioRunner{
		cfg:      cfg,
		client:   client,
		receiver: receiver,
		ids:      make(map[string]resolvedID),
		cleanup:  nil,
	}

	// Execute the scenario.
	scenarioStart := time.Now()

	results, runErr := runner.run(ctx, scenario)

	scenarioElapsed := time.Since(scenarioStart)

	// Print results to stdout.
	printer := NewOutputPrinter(os.Stdout)
	printer.PrintScenario(scenario.Name, scenarioElapsed, runErr, results)

	// Write JUnit XML if requested.
	if cfg.JUnitPath != "" {
		if xmlErr := WriteJUnit(cfg.JUnitPath, scenario.Name, results, scenarioElapsed); xmlErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to write JUnit XML: %v\n", xmlErr)
		}
	}

	return runErr
}

// resolvedID holds the UID and slug for a seeded resource.
type resolvedID struct {
	uid  string
	slug string
}

// cleanupFn is a deferred cleanup function.
type cleanupFn struct {
	name string
	fn   func(ctx context.Context) error
}

// scenarioRunner holds runtime state across steps.
type scenarioRunner struct {
	cfg      RunConfig
	client   *apiClient
	receiver *WebhookReceiver
	ids      map[string]resolvedID // step id → {uid, slug}
	cleanup  []cleanupFn           // LIFO cleanup stack
}

// resolveID looks up an id previously registered by a create step.
func (r *scenarioRunner) resolveID(id string) (resolvedID, error) {
	rid, ok := r.ids[id]
	if !ok {
		return resolvedID{}, fmt.Errorf("unknown id %q (check step ordering)", id)
	}

	return rid, nil
}

// pushCleanup appends a cleanup function to the LIFO stack.
func (r *scenarioRunner) pushCleanup(name string, fn func(ctx context.Context) error) {
	r.cleanup = append(r.cleanup, cleanupFn{name: name, fn: fn})
}

// runCleanup executes all cleanup functions in LIFO order.
func (r *scenarioRunner) runCleanup(ctx context.Context) []stepResult {
	results := make([]stepResult, 0, len(r.cleanup))

	for i := len(r.cleanup) - 1; i >= 0; i-- {
		cf := r.cleanup[i]
		start := time.Now()
		err := cf.fn(ctx)
		results = append(results, stepResult{
			Name:     "cleanup: " + cf.name,
			Duration: time.Since(start),
			Err:      err,
		})
	}

	return results
}

// run executes all scenario steps and cleanup, returning per-step results and
// the first step error encountered.
func (r *scenarioRunner) run(ctx context.Context, scenario *ScenarioFile) ([]stepResult, error) {
	results := make([]stepResult, 0, len(scenario.Steps)+1)
	var firstErr error

	for _, step := range scenario.Steps {
		start := time.Now()
		name, err := r.execStep(ctx, step)
		elapsed := time.Since(start)

		sr := stepResult{Name: name, Duration: elapsed, Err: err}
		results = append(results, sr)

		if err != nil && firstErr == nil {
			firstErr = err
			// Continue cleanup but stop executing further steps.
			break
		}
	}

	if scenario.Cleanup {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		cleanupResults := r.runCleanup(cleanupCtx)
		results = append(results, cleanupResults...)
	}

	return results, firstErr
}

// execStep dispatches a single YAML step and returns a display name + error.
func (r *scenarioRunner) execStep(ctx context.Context, step StepYAML) (string, error) {
	switch step.Kind {
	case "create_heartbeat_check":
		return r.stepCreateHeartbeatCheck(ctx, step)

	case "create_webhook_channel":
		return r.stepCreateWebhookChannel(ctx, step)

	case "create_escalation_policy":
		return r.stepCreateEscalationPolicy(ctx, step)

	case "assign_policy_to_check":
		return r.stepAssignPolicyToCheck(ctx, step)

	case "send_heartbeat":
		return r.stepSendHeartbeat(ctx, step)

	case "expect_webhook":
		return r.stepExpectWebhook(ctx, step)

	case "assert_no_webhook":
		return r.stepAssertNoWebhook(ctx, step)

	case "assert_incident_open":
		return r.stepAssertIncidentOpen(ctx, step)

	case "wait":
		return r.stepWait(ctx, step)

	default:
		return step.Kind, fmt.Errorf("unknown step kind %q", step.Kind)
	}
}

func (r *scenarioRunner) stepCreateHeartbeatCheck(ctx context.Context, step StepYAML) (string, error) {
	name := "create_heartbeat_check"
	if step.ID != "" {
		name += " " + step.ID
	}

	slug := step.Slug
	if slug == "" {
		slug = step.ID
	}

	res, err := CreateHeartbeatCheck(ctx, r.client, r.cfg.Org, slug,
		step.ConfirmationPeriodSeconds, step.RecoveryPeriodSeconds, step.EscalationThreshold)
	if err != nil {
		return name, err
	}

	if step.ID != "" {
		r.ids[step.ID] = resolvedID{uid: res.UID, slug: res.Slug}
		// Store the heartbeat token under "<id>_token" for send_heartbeat steps.
		r.ids[step.ID+"_token"] = resolvedID{uid: res.Token}
	}

	if res.Token != "" {
		// Store token by slug too.
		r.ids[res.Slug+"_token"] = resolvedID{uid: res.Token}
	}

	// Push cleanup: delete by slug.
	checkSlug := res.Slug
	r.pushCleanup("delete check "+checkSlug, func(ctx context.Context) error {
		return DeleteCheck(ctx, r.client, r.cfg.Org, checkSlug)
	})

	return name, nil
}

func (r *scenarioRunner) stepCreateWebhookChannel(ctx context.Context, step StepYAML) (string, error) {
	name := "create_webhook_channel"
	if step.ID != "" {
		name += " " + step.ID
	}

	channelName := step.ID
	if channelName == "" {
		channelName = "scenario-channel"
	}

	res, err := CreateWebhookChannel(ctx, r.client, r.cfg.Org, channelName, step.URL)
	if err != nil {
		return name, err
	}

	if step.ID != "" {
		r.ids[step.ID] = resolvedID{uid: res.UID}
	}

	// Push cleanup: delete by UID.
	channelUID := res.UID
	r.pushCleanup("delete channel "+channelUID, func(ctx context.Context) error {
		return DeleteChannel(ctx, r.client, r.cfg.Org, channelUID)
	})

	return name, nil
}

func (r *scenarioRunner) stepCreateEscalationPolicy(ctx context.Context, step StepYAML) (string, error) {
	name := "create_escalation_policy"
	if step.ID != "" {
		name += " " + step.ID
	}

	slug := step.Slug
	if slug == "" {
		slug = step.ID
	}

	// Resolve channel IDs in targets to actual UIDs.
	apiSteps := make([]EscalationPolicyStep, 0, len(step.Steps))
	for _, s := range step.Steps {
		targets := make([]EscalationPolicyStepTarget, 0, len(s.Targets))
		for _, tgt := range s.Targets {
			targetUID := ""
			if tgt.ChannelID != "" {
				rid, err := r.resolveID(tgt.ChannelID)
				if err != nil {
					return name, fmt.Errorf("resolve channel_id %q: %w", tgt.ChannelID, err)
				}

				targetUID = rid.uid
			}

			targets = append(targets, EscalationPolicyStepTarget{
				Type:      tgt.Type,
				TargetUID: targetUID,
			})
		}

		apiSteps = append(apiSteps, EscalationPolicyStep{
			DelaySeconds: s.DelaySeconds,
			Targets:      targets,
		})
	}

	res, err := CreateEscalationPolicy(ctx, r.client, r.cfg.Org, slug, apiSteps,
		step.RepeatMax, step.RepeatAfterSeconds)
	if err != nil {
		return name, err
	}

	if step.ID != "" {
		r.ids[step.ID] = resolvedID{uid: res.UID, slug: res.Slug}
	}

	// Push cleanup: delete by slug.
	policySlug := res.Slug
	r.pushCleanup("delete policy "+policySlug, func(ctx context.Context) error {
		return DeletePolicy(ctx, r.client, r.cfg.Org, policySlug)
	})

	return name, nil
}

func (r *scenarioRunner) stepAssignPolicyToCheck(ctx context.Context, step StepYAML) (string, error) {
	name := "assign_policy_to_check"

	checkRID, err := r.resolveID(step.CheckID)
	if err != nil {
		return name, fmt.Errorf("resolve check_id %q: %w", step.CheckID, err)
	}

	policyRID, err := r.resolveID(step.PolicyID)
	if err != nil {
		return name, fmt.Errorf("resolve policy_id %q: %w", step.PolicyID, err)
	}

	// Use check slug for the PATCH endpoint; fall back to UID.
	checkRef := checkRID.slug
	if checkRef == "" {
		checkRef = checkRID.uid
	}

	if err := AssignPolicyToCheck(ctx, r.client, r.cfg.Org, checkRef, policyRID.uid); err != nil {
		return name, err
	}

	return name, nil
}

func (r *scenarioRunner) stepSendHeartbeat(ctx context.Context, step StepYAML) (string, error) {
	name := "send_heartbeat (" + step.Status + ")"

	checkRID, err := r.resolveID(step.CheckID)
	if err != nil {
		return name, fmt.Errorf("resolve check_id %q: %w", step.CheckID, err)
	}

	// Look up the token stored under <id>_token.
	tokenRID, err := r.resolveID(step.CheckID + "_token")
	if err != nil {
		return name, fmt.Errorf("no heartbeat token for %q (make sure create_heartbeat_check ran first): %w",
			step.CheckID, err)
	}

	checkSlug := checkRID.slug
	if checkSlug == "" {
		checkSlug = checkRID.uid
	}

	if err := SendHeartbeat(ctx, r.client, r.cfg.Org, checkSlug, tokenRID.uid, step.Status); err != nil {
		return name, err
	}

	return name, nil
}

func (r *scenarioRunner) stepExpectWebhook(ctx context.Context, step StepYAML) (string, error) {
	channelID := step.ChannelID
	desc := describeAssert(step.Assert)
	name := "expect_webhook (" + desc + ")"

	predicate := buildAssertPredicate(step.Assert)

	if err := assertWebhook(ctx, r.receiver, channelID, desc, predicate, step.Timeout); err != nil {
		return name, err
	}

	return name, nil
}

func (r *scenarioRunner) stepAssertNoWebhook(_ context.Context, step StepYAML) (string, error) {
	name := "assert_no_webhook"
	waitDuration := step.Duration
	if waitDuration <= 0 {
		waitDuration = step.Timeout
	}

	if waitDuration <= 0 {
		waitDuration = 3 * time.Second
	}

	channelID := step.ChannelID
	received := make(chan struct{}, 1)

	// Wait in a separate goroutine.
	checkCtx, cancel := context.WithTimeout(context.Background(), waitDuration)
	defer cancel()

	go func() {
		if err := r.receiver.WaitForEvent(checkCtx, channelID, func(_ map[string]any) bool { return true }); err == nil {
			received <- struct{}{}
		}
	}()

	select {
	case <-received:
		return name, fmt.Errorf("unexpected webhook received on channel %q within %s", channelID, waitDuration)
	case <-checkCtx.Done():
		// Context expired without a webhook — that's the expected outcome.
		return name, nil
	}
}

func (r *scenarioRunner) stepAssertIncidentOpen(ctx context.Context, step StepYAML) (string, error) {
	name := "assert_incident_open"

	checkRID, err := r.resolveID(step.CheckID)
	if err != nil {
		return name, fmt.Errorf("resolve check_id %q: %w", step.CheckID, err)
	}

	checkUID := checkRID.uid

	var out map[string]any

	_, err = r.client.do(ctx, http.MethodGet,
		fmt.Sprintf("/api/v1/orgs/%s/incidents?checkUid=%s&state=active", r.cfg.Org, checkUID),
		nil, &out)
	if err != nil {
		return name, fmt.Errorf("list incidents: %w", err)
	}

	data, _ := out["data"].([]any)
	if len(data) == 0 {
		return name, fmt.Errorf("no open incident found for check %q", step.CheckID)
	}

	return name, nil
}

func (r *scenarioRunner) stepWait(_ context.Context, step StepYAML) (string, error) {
	name := fmt.Sprintf("wait %s", step.Duration)

	if step.Duration > 0 {
		time.Sleep(step.Duration)
	}

	return name, nil
}

// loadScenario reads, template-expands, and unmarshals a YAML scenario file.
func loadScenario(path string, data TemplateData) (*ScenarioFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	// Template expansion.
	tpl, err := template.New("scenario").Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("parse template in %s: %w", path, err)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("expand template in %s: %w", path, err)
	}

	var scenario ScenarioFile
	if err := yaml.Unmarshal(buf.Bytes(), &scenario); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}

	return &scenario, nil
}
