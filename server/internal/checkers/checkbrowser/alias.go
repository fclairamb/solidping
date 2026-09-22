package checkbrowser

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkbrowser/config"

// BrowserConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkbrowser.BrowserConfig` call site compiling.
type BrowserConfig = checkconfig.BrowserConfig

// ValidateNavigationURL validates a URL a browser step may navigate to. It
// forwards to the config sub-package that owns the rule, keeping the existing
// `checkbrowser.ValidateNavigationURL` spelling for checkjs.
func ValidateNavigationURL(rawURL string) error { return checkconfig.ValidateNavigationURL(rawURL) }
