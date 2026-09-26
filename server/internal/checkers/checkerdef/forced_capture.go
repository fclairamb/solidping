package checkerdef

import "context"

// forcedCaptureCtxKeyType makes the context key unique within the binary.
type forcedCaptureCtxKeyType struct{}

//nolint:gochecknoglobals // sentinel value for a context key
var forcedCaptureCtxKey = forcedCaptureCtxKeyType{}

// WithForcedCapture marks one execution as an on-demand capture (spec
// 2026-09-25-34, "Capture now"): a checker that can photograph its target keeps
// the capture whatever the verdict, and whether or not the check opted into
// failure screenshots. The worker sets it from the claimed job's
// capture_requested_at; checkers only consume it. Mirrors WithIPVersion.
//
// It changes WHAT is kept, never the verdict: an on-demand run of a healthy
// page is still `up`, and its result goes through the incident pipeline like
// any other run.
func WithForcedCapture(ctx context.Context) context.Context {
	return context.WithValue(ctx, forcedCaptureCtxKey, true)
}

// ForcedCapture reports whether ctx carries an on-demand capture request.
func ForcedCapture(ctx context.Context) bool {
	forced, ok := ctx.Value(forcedCaptureCtxKey).(bool)

	return ok && forced
}
