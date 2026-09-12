package checksmtp

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
