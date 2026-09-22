package config

// The DNS record types `record_type` accepts. They are part of the config
// vocabulary, so they live here rather than with the resolver that queries
// them — an offline validator must know the closed set without linking it.
const (
	RecordTypeA     = "A"
	RecordTypeAAAA  = "AAAA"
	RecordTypeCNAME = "CNAME"
	RecordTypeMX    = "MX"
	RecordTypeNS    = "NS"
	RecordTypeTXT   = "TXT"
	RecordTypeSOA   = "SOA"

	// DefaultRecordType is the type queried when the config sets none.
	DefaultRecordType = RecordTypeA
)

// validRecordTypes is the closed set above, as a lookup.
//
//nolint:gochecknoglobals // constant lookup map
var validRecordTypes = map[string]bool{
	RecordTypeA:     true,
	RecordTypeAAAA:  true,
	RecordTypeCNAME: true,
	RecordTypeMX:    true,
	RecordTypeNS:    true,
	RecordTypeTXT:   true,
	RecordTypeSOA:   true,
}

// IsValidRecordType reports whether s (upper-cased) is a supported record type.
func IsValidRecordType(s string) bool { return validRecordTypes[s] }
