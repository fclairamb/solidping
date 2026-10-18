package plugins

import (
	"context"
	"fmt"
	"os"

	"github.com/dop251/goja"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkjs"
)

// maxScriptBytes is the js check's own script cap, applied to an entry file.
const maxScriptBytes = 64 * 1024

// jsTransport runs a plugin's entry script in the checkjs runtime.
type jsTransport struct {
	script string
}

func newJSTransport(entryPath string) (*jsTransport, error) {
	raw, err := os.ReadFile(entryPath)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrEntryMissing, err)
	}

	if len(raw) > maxScriptBytes {
		return nil, fmt.Errorf("%w: entry is %d bytes, the cap is %d", ErrInvalidManifest, len(raw), maxScriptBytes)
	}

	script := string(raw)
	if _, err := goja.Compile("entry", "(function() {\n"+script+"\n})()", true); err != nil {
		return nil, fmt.Errorf("%w: entry syntax error: %w", ErrInvalidManifest, err)
	}

	return &jsTransport{script: script}, nil
}

func (t *jsTransport) execute(ctx context.Context, _ string, cfg *Config) (*checkerdef.Result, error) {
	result, err := (&checkjs.JSChecker{}).Execute(ctx, &checkjs.JSConfig{
		Script:       t.script,
		Timeout:      cfg.timeout(),
		PluginConfig: cfg.GetConfig(),
	})
	if err != nil {
		return nil, fmt.Errorf("running js plugin: %w", err)
	}

	return result, nil
}

func (t *jsTransport) close() {}
