package checksmtp

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checksmtp/config"

// SMTPConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checksmtp.SMTPConfig` call site compiling.
type SMTPConfig = checkconfig.SMTPConfig

// Defined with the config; aliased so this package keeps the short name.
const (
	defaultPort     = checkconfig.DefaultPort
	defaultTimeout  = checkconfig.DefaultTimeout
	implicitTLSPort = checkconfig.ImplicitTLSPort
)

// ValidateMailFrom validates an envelope sender, forwarding to the config
// sub-package that owns the rule.
func ValidateMailFrom(mailFrom string) error { return checkconfig.ValidateMailFrom(mailFrom) }

// ValidateDeliveryTo validates a delivery recipient, forwarding to the config
// sub-package that owns the rule.
func ValidateDeliveryTo(deliveryTo string) error { return checkconfig.ValidateDeliveryTo(deliveryTo) }
