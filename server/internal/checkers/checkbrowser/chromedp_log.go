package checkbrowser

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/chromedp/chromedp"
)

// chromedpLogOptions routes chromedp's own logging into slog. By default it
// prints through the stdlib log package, so an "ERROR: ..." line lands at INFO
// with a frame id in the message, which can neither be filtered nor grouped.
func chromedpLogOptions(logger *slog.Logger) []chromedp.ContextOption {
	return []chromedp.ContextOption{
		chromedp.WithErrorf(func(format string, args ...any) {
			logChromedpError(logger, fmt.Sprintf(format, args...))
		}),
		chromedp.WithLogf(func(format string, args ...any) {
			logger.DebugContext(context.Background(), "chromedp", "message", fmt.Sprintf(format, args...))
		}),
	}
}

// logChromedpError logs one chromedp error line. A page torn down mid
// navigation makes chromedp fail to read its document root; that is expected
// and only worth a DEBUG.
func logChromedpError(logger *slog.Logger, text string) {
	ctx := context.Background()

	if strings.Contains(text, "could not retrieve document root") {
		logger.DebugContext(ctx, "chromedp error", "error", text)

		return
	}

	logger.WarnContext(ctx, "chromedp error", "error", text)
}
