package checkbrowser

import (
	"context"
	"strings"
	"time"

	"github.com/chromedp/chromedp"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const microsecondsPerMilli = 1000.0

// BrowserChecker implements the Checker interface for headless Chrome browser checks.
type BrowserChecker struct{}

// Type returns the check type identifier.
func (c *BrowserChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeBrowser
}

// Validate checks if the configuration is valid.
func (c *BrowserChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &BrowserConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Name == "" {
		spec.Name = hostnameFromURL(cfg.URL)
	}

	if spec.Slug == "" {
		spec.Slug = "browser-" + strings.ReplaceAll(hostnameFromURL(cfg.URL), ".", "-")
	}

	return nil
}

// Execute performs the browser health check and returns the result.
func (c *BrowserChecker) Execute(
	ctx context.Context,
	config checkerdef.Config,
) (*checkerdef.Result, error) {
	cfg, ok := config.(*BrowserConfig)
	if !ok {
		return nil, ErrInvalidConfigType
	}

	timeout := cfg.resolveTimeout()

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()

	metrics := map[string]any{}
	output := map[string]any{
		"url": cfg.URL,
	}

	result := c.runBrowser(ctx, cfg, start, metrics, output)

	return result, nil
}

func (c *BrowserChecker) runBrowser(
	ctx context.Context,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	allocCtx, allocCancel := chromedp.NewExecAllocator(ctx, chromedp.DefaultExecAllocatorOptions[:]...)
	defer allocCancel()

	browserCtx, browserCancel := chromedp.NewContext(allocCtx)
	defer browserCancel()

	result := c.navigateAndCheck(browserCtx, cfg, start, metrics, output)

	return result
}

func (c *BrowserChecker) navigateAndCheck(
	ctx context.Context,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	var title string

	navStart := time.Now()

	actions := []chromedp.Action{
		chromedp.Navigate(cfg.URL),
	}

	if cfg.WaitSelector != "" {
		actions = append(actions, chromedp.WaitVisible(cfg.WaitSelector))
	} else {
		actions = append(actions, chromedp.WaitReady("body"))
	}

	actions = append(actions, chromedp.Title(&title))

	if err := chromedp.Run(ctx, actions...); err != nil {
		return c.handleBrowserError(ctx, err, start, metrics, output)
	}

	metrics["load_time_ms"] = durationMs(time.Since(navStart))

	output["title"] = title

	if cfg.Keyword != "" {
		return c.checkKeyword(ctx, cfg, start, metrics, output)
	}

	metrics["total_time_ms"] = durationMs(time.Since(start))

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *BrowserChecker) checkKeyword(
	ctx context.Context,
	cfg *BrowserConfig,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	var bodyText string
	if err := chromedp.Run(ctx, chromedp.Text("body", &bodyText)); err != nil {
		output["error"] = "failed to read page text: " + err.Error()

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	found := strings.Contains(bodyText, cfg.Keyword)
	if cfg.InvertKeyword {
		found = !found
	}

	output["keywordFound"] = found
	metrics["total_time_ms"] = durationMs(time.Since(start))

	if !found {
		output["error"] = "keyword check failed"

		return &checkerdef.Result{
			Status:   checkerdef.StatusDown,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	return &checkerdef.Result{
		Status:   checkerdef.StatusUp,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func (c *BrowserChecker) handleBrowserError(
	ctx context.Context,
	err error,
	start time.Time,
	metrics map[string]any,
	output map[string]any,
) *checkerdef.Result {
	if ctx.Err() != nil {
		output["error"] = "browser check timed out"

		return &checkerdef.Result{
			Status:   checkerdef.StatusTimeout,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	errMsg := err.Error()

	// Detect Chrome not installed
	if strings.Contains(errMsg, "exec") || strings.Contains(errMsg, "not found") ||
		strings.Contains(errMsg, "no such file") {
		output["error"] = "Chrome/Chromium not found: ensure headless Chrome is installed"

		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Metrics:  metrics,
			Output:   output,
		}
	}

	output["error"] = errMsg

	return &checkerdef.Result{
		Status:   checkerdef.StatusDown,
		Duration: time.Since(start),
		Metrics:  metrics,
		Output:   output,
	}
}

func durationMs(d time.Duration) float64 {
	return float64(d.Microseconds()) / microsecondsPerMilli
}
