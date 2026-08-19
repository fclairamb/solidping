package checksmtp

import (
	"net/mail"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	defaultPort       = 25
	defaultTimeout    = 10 * time.Second
	maxTimeout        = 30 * time.Second
	defaultEHLODomain = "solidping.local"
	implicitTLSPort   = 465
)

// SMTPConfig holds the configuration for SMTP server checks.
type SMTPConfig struct {
	Host           string        `json:"host"`
	Port           int           `json:"port,omitempty"`
	Timeout        time.Duration `json:"timeout,omitempty"`
	StartTLS       bool          `json:"starttls,omitempty"`
	TLSVerify      bool          `json:"tls_verify,omitempty"`      //nolint:tagliatelle // API uses snake_case
	TLSServerName  string        `json:"tls_server_name,omitempty"` //nolint:tagliatelle // API uses snake_case
	EHLODomain     string        `json:"ehlo_domain,omitempty"`     //nolint:tagliatelle // API uses snake_case
	ExpectGreeting string        `json:"expect_greeting,omitempty"` //nolint:tagliatelle // API uses snake_case
	CheckAuth      bool          `json:"check_auth,omitempty"`      //nolint:tagliatelle // API uses snake_case
	Username       string        `json:"username,omitempty"`
	Password       string        `json:"password,omitempty"`

	// SendEmail opts this check into send mode: after the normal
	// EHLO/STARTTLS/AUTH sequence, submit a system-generated probe email
	// addressed to DeliveryTo. Default false; existing checks are completely
	// unaffected.
	SendEmail bool `json:"send_email,omitempty"` //nolint:tagliatelle // API uses snake_case
	// MailFrom is the envelope sender for the probe email. Required when
	// SendEmail is set — the monitored server's sender policy usually
	// dictates it (SPF/DKIM alignment, relay ACLs, etc.).
	MailFrom string `json:"mail_from,omitempty"` //nolint:tagliatelle // API uses snake_case
	// DeliveryTo is the recipient address for the probe email — the
	// OPERATIVE field for send mode (revised design, 2026-08-19). Stored
	// directly rather than resolved from a reference at dispatch time: that
	// resolution only ever worked in-process (DirectBackend), so send mode
	// never worked on a private-location/deported agent, and wiring
	// resolution into the agent claim path raised an unresolved question
	// about handing a resolved recipient to a system agent shared across
	// orgs. Storing the address directly sidesteps both — it flows to every
	// worker/agent through the normal config path, no claim-time resolution
	// or context threading needed. Required when SendEmail is set; must be a
	// bare RFC 5322 address (ValidateDeliveryTo) whose domain equals the
	// instance's configured email_inbox domain (enforced server-side in
	// checks/service.go, which has DB access this package doesn't).
	//
	// Accepted residual risk (Florent, 2026-08-19): giving up the
	// reference-not-address indirection means one org that learns another
	// org's tokenized address can aim probes at it. The domain restriction is
	// what still holds — a non-instance recipient remains impossible.
	DeliveryTo string `json:"delivery_to,omitempty"` //nolint:tagliatelle // API uses snake_case
	// DeliveryCheckUID is OPTIONAL bonus metadata (revised design,
	// 2026-08-19): purely for attribution and the dashboard's pairing links
	// ("delivery via" / "delivery sources"). It is NOT a security control and
	// resolves nothing — DeliveryTo above is what the probe is actually
	// addressed to. If supplied it must still name an existing CheckTypeEmail
	// check in the same org (checks/service.go), so the bonus metadata itself
	// can't become a cross-org information leak, but it is never required.
	DeliveryCheckUID string `json:"delivery_check_uid,omitempty"` //nolint:tagliatelle // API uses snake_case
}

// FromMap populates the configuration from a map.
//
//nolint:cyclop // Configuration parsing requires checking multiple field types
func (c *SMTPConfig) FromMap(configMap map[string]any) error {
	if host, ok := configMap[checkerdef.OutputKeyHost].(string); ok {
		c.Host = host
	} else if configMap[checkerdef.OutputKeyHost] != nil {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "must be a string")
	}

	if port, ok := configMap["port"].(int); ok {
		c.Port = port
	} else if portFloat, ok := configMap["port"].(float64); ok {
		c.Port = int(portFloat)
	} else if configMap["port"] != nil {
		return checkerdef.NewConfigError("port", "must be a number")
	}

	if timeout, ok := configMap["timeout"].(string); ok {
		duration, err := time.ParseDuration(timeout)
		if err != nil {
			return checkerdef.NewConfigError("timeout", "must be a valid duration string")
		}

		c.Timeout = duration
	} else if configMap["timeout"] != nil {
		return checkerdef.NewConfigError("timeout", "must be a string")
	}

	if starttls, ok := configMap["starttls"].(bool); ok {
		c.StartTLS = starttls
	} else if configMap["starttls"] != nil {
		return checkerdef.NewConfigError("starttls", "must be a boolean")
	}

	if tlsVerify, ok := configMap["tls_verify"].(bool); ok {
		c.TLSVerify = tlsVerify
	} else if configMap["tls_verify"] != nil {
		return checkerdef.NewConfigError("tls_verify", "must be a boolean")
	}

	if tlsServerName, ok := configMap["tls_server_name"].(string); ok {
		c.TLSServerName = tlsServerName
	} else if configMap["tls_server_name"] != nil {
		return checkerdef.NewConfigError("tls_server_name", "must be a string")
	}

	if ehloDomain, ok := configMap["ehlo_domain"].(string); ok {
		c.EHLODomain = ehloDomain
	} else if configMap["ehlo_domain"] != nil {
		return checkerdef.NewConfigError("ehlo_domain", "must be a string")
	}

	if expectGreeting, ok := configMap["expect_greeting"].(string); ok {
		c.ExpectGreeting = expectGreeting
	} else if configMap["expect_greeting"] != nil {
		return checkerdef.NewConfigError("expect_greeting", "must be a string")
	}

	if checkAuth, ok := configMap["check_auth"].(bool); ok {
		c.CheckAuth = checkAuth
	} else if configMap["check_auth"] != nil {
		return checkerdef.NewConfigError("check_auth", "must be a boolean")
	}

	if username, ok := configMap["username"].(string); ok {
		c.Username = username
	} else if configMap["username"] != nil {
		return checkerdef.NewConfigError("username", "must be a string")
	}

	if password, ok := configMap["password"].(string); ok {
		c.Password = password
	} else if configMap["password"] != nil {
		return checkerdef.NewConfigError("password", "must be a string")
	}

	return c.fromMapSendMode(configMap)
}

// fromMapSendMode parses the send-mode fields (send_email/mail_from/
// delivery_to/delivery_check_uid), split out of FromMap to keep its
// complexity down.
func (c *SMTPConfig) fromMapSendMode(configMap map[string]any) error {
	if sendEmail, ok := configMap["send_email"].(bool); ok {
		c.SendEmail = sendEmail
	} else if configMap["send_email"] != nil {
		return checkerdef.NewConfigError("send_email", "must be a boolean")
	}

	if mailFrom, ok := configMap["mail_from"].(string); ok {
		c.MailFrom = mailFrom
	} else if configMap["mail_from"] != nil {
		return checkerdef.NewConfigError("mail_from", "must be a string")
	}

	if deliveryTo, ok := configMap["delivery_to"].(string); ok {
		c.DeliveryTo = deliveryTo
	} else if configMap["delivery_to"] != nil {
		return checkerdef.NewConfigError("delivery_to", "must be a string")
	}

	if deliveryCheckUID, ok := configMap["delivery_check_uid"].(string); ok {
		c.DeliveryCheckUID = deliveryCheckUID
	} else if configMap["delivery_check_uid"] != nil {
		return checkerdef.NewConfigError("delivery_check_uid", "must be a string")
	}

	return nil
}

// GetConfig returns the configuration as a map.
func (c *SMTPConfig) GetConfig() map[string]any {
	cfg := map[string]any{
		checkerdef.OutputKeyHost: c.Host,
	}

	if c.Port != 0 && c.Port != defaultPort {
		cfg["port"] = c.Port
	}

	if c.Timeout != 0 {
		cfg["timeout"] = c.Timeout.String()
	}

	if c.StartTLS {
		cfg["starttls"] = c.StartTLS
	}

	if c.TLSVerify {
		cfg["tls_verify"] = c.TLSVerify
	}

	if c.TLSServerName != "" {
		cfg["tls_server_name"] = c.TLSServerName
	}

	if c.EHLODomain != "" && c.EHLODomain != defaultEHLODomain {
		cfg["ehlo_domain"] = c.EHLODomain
	}

	if c.ExpectGreeting != "" {
		cfg["expect_greeting"] = c.ExpectGreeting
	}

	if c.CheckAuth {
		cfg["check_auth"] = c.CheckAuth
	}

	if c.Username != "" {
		cfg["username"] = c.Username
	}

	if c.Password != "" {
		cfg["password"] = c.Password
	}

	c.getConfigSendMode(cfg)

	return cfg
}

// getConfigSendMode appends the send-mode fields, split out of GetConfig to
// keep its complexity down.
func (c *SMTPConfig) getConfigSendMode(cfg map[string]any) {
	if c.SendEmail {
		cfg["send_email"] = c.SendEmail
	}

	if c.MailFrom != "" {
		cfg["mail_from"] = c.MailFrom
	}

	if c.DeliveryTo != "" {
		cfg["delivery_to"] = c.DeliveryTo
	}

	if c.DeliveryCheckUID != "" {
		cfg["delivery_check_uid"] = c.DeliveryCheckUID
	}
}

// Validate checks if the configuration is valid.
func (c *SMTPConfig) Validate() error {
	if c.Host == "" {
		return checkerdef.NewConfigError(checkerdef.OutputKeyHost, "is required")
	}

	if c.Port < 0 || c.Port > 65535 {
		return checkerdef.NewConfigErrorf("port", "must be between 1 and 65535, got %d", c.Port)
	}

	if c.Timeout != 0 && (c.Timeout <= 0 || c.Timeout > maxTimeout) {
		return checkerdef.NewConfigErrorf("timeout", "must be > 0 and <= 30s, got %s", c.Timeout.String())
	}

	if c.StartTLS && c.Port == implicitTLSPort {
		return checkerdef.NewConfigError("starttls", "cannot use STARTTLS with port 465 (implicit TLS)")
	}

	// Shape-only: send mode requires mail_from and delivery_to, both
	// validated as real addresses (never just non-empty — see
	// validateAddressField). delivery_to's domain-matches-email_inbox rule
	// and delivery_check_uid's cross-check reference (existence, same-org,
	// CheckTypeEmail, when supplied — it is optional bonus metadata, revised
	// design 2026-08-19) need DB access this layer doesn't have — those live
	// in checks/service.go's validateSMTPDeliveryConfig, which runs on both
	// the create and PATCH paths (mirroring how tunnelCheckUid splits the
	// same way).
	//
	// mail_from and delivery_to are both spliced verbatim into the wire
	// protocol (checker.go): mail_from into the MAIL FROM command and the
	// From: header, delivery_to into RCPT TO and the To: header. Validating
	// both as real RFC 5322 addresses here (not just non-empty) is what stops
	// either from being used to smuggle a CRLF-terminated extra SMTP command
	// or an injected header — see validateAddressField.
	if c.SendEmail {
		if err := ValidateMailFrom(c.MailFrom); err != nil {
			return err
		}

		if err := ValidateDeliveryTo(c.DeliveryTo); err != nil {
			return err
		}
	}

	return nil
}

// validateAddressField is the shared address-format check behind both
// ValidateMailFrom and ValidateDeliveryTo: mail.ParseAddress must succeed AND
// the parsed addr.Address must equal the input verbatim. This is what keeps
// CRLF, NUL, U+2028, display-name forms, and quoted-local-parts containing
// '>' out of the SMTP command line and the header block for EITHER field —
// mirrors the net/mail.ParseAddress check used elsewhere in the repo
// (statussubscribers.normalizeEmail). ParseAddress alone is not enough: it
// happily accepts "Display Name <addr>", which would let the "Display Name"
// portion carry attacker-controlled text into the wire value verbatim (it is
// not itself CRLF, so ParseAddress accepts it, but it still is NOT the bare
// address the caller asked to store) — the equality check is what forces the
// stored/wire value to be exactly the bare address, nothing else.
func validateAddressField(field, value string) error {
	if value == "" {
		return checkerdef.NewConfigError(field, "is required when send_email is set")
	}

	addr, err := mail.ParseAddress(value)
	if err != nil {
		return checkerdef.NewConfigError(field, "must be a single valid email address")
	}

	if addr.Address != value {
		return checkerdef.NewConfigError(field, "must be a bare email address, not a display name + address")
	}

	return nil
}

// ValidateMailFrom requires a non-empty, single bare RFC 5322 address — see
// validateAddressField for what that rules out and why.
func ValidateMailFrom(mailFrom string) error {
	return validateAddressField("mail_from", mailFrom)
}

// ValidateDeliveryTo requires a non-empty, single bare RFC 5322 address — the
// same rule as ValidateMailFrom (see validateAddressField), since delivery_to
// is now the operative send-mode recipient (revised design, 2026-08-19) and
// therefore carries exactly the same injection surface mail_from does.
func ValidateDeliveryTo(deliveryTo string) error {
	return validateAddressField("delivery_to", deliveryTo)
}
