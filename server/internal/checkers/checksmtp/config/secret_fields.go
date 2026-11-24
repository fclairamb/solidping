package config

// SecretFields declares which top-level config keys carry secrets and must
// be encrypted at rest. Implements credentials.SecretFielder.
func (c *SMTPConfig) SecretFields() []string {
	return []string{"password"}
}

// ExportRedactedFields declares the config keys that are public at rest but
// must never be written into a config-as-code export. Implements
// credentials.ExportRedactedFielder.
//
// `delivery_to` is a send-mode check's probe recipient, and on this instance it
// is an email check's tokenized inbound address — the SAME 48-hex-char secret
// checkemail redacts, carried a second time (spec 2026-09-11-02). Redacting it
// costs nothing, because the value is fully derivable: `delivery_check_uid`
// names the email check it belongs to and stays exported, so import/apply
// re-derives the address from that check's token (checks.deriveRedactedFields)
// when the document omits it.
//
// `password` is NOT listed here: it is a declared secret (SecretFields above),
// so the exporter already strips it.
func (c *SMTPConfig) ExportRedactedFields() []string {
	return []string{"delivery_to"}
}

// TargetFields lists the config keys that decide where the secrets go: a
// browser-sealed blob is bound to them (spec 2026-11-11-01). The Unix socket
// binds too: a socket config leaves the network target empty or fixed, so
// without it a sealed blob could be pointed at another socket (spec
// 2026-11-24-10).
func (c *SMTPConfig) TargetFields() []string {
	return []string{"host", "port", configKeySocket}
}

// LegacyTargetFields is the list blobs were bound to before the Unix socket
// was a target field (spec 2026-11-24-10).
func (c *SMTPConfig) LegacyTargetFields() []string {
	return []string{"host", "port"}
}
