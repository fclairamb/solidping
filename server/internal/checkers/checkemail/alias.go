package checkemail

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkemail/config"

// EmailConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkemail.EmailConfig` call site compiling.
type EmailConfig = checkconfig.EmailConfig
