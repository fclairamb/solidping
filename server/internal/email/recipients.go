package email

import (
	"net/mail"
	"strings"
)

// ValidAddress reports whether address parses as an RFC 5322 address.
func ValidAddress(address string) bool {
	_, err := mail.ParseAddress(strings.TrimSpace(address))

	return err == nil
}

// ValidRecipients splits addresses into those the mail sender can parse and
// those it cannot. Producers of email jobs filter through it before enqueueing,
// so an unparseable stored address is skipped instead of failing at send time.
func ValidRecipients(addresses []string) ([]string, []string) {
	var valid, invalid []string

	for _, address := range addresses {
		if ValidAddress(address) {
			valid = append(valid, address)
		} else {
			invalid = append(invalid, address)
		}
	}

	return valid, invalid
}

// RedactAddress hides an address for logs: only the domain part of a
// well-formed address survives ("***@example.com"); anything else is "***".
func RedactAddress(address string) string {
	at := strings.LastIndex(address, "@")
	if at < 0 || at == len(address)-1 {
		return "***"
	}

	return "***" + address[at:]
}

// RedactAddresses applies RedactAddress to each address.
func RedactAddresses(addresses []string) []string {
	out := make([]string, 0, len(addresses))
	for _, address := range addresses {
		out = append(out, RedactAddress(address))
	}

	return out
}
