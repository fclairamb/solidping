package checkdns

import checkconfig "github.com/fclairamb/solidping/server/internal/checkers/checkdns/config"

// DNSConfig is this check's configuration. It lives in the light `config`
// sub-package; this alias keeps every existing `checkdns.DNSConfig` call site compiling.
type DNSConfig = checkconfig.DNSConfig

// The record-type vocabulary lives with the config (it is what `record_type`
// accepts); these unexported aliases keep this package's own call sites short.
const (
	defaultRecordType = checkconfig.DefaultRecordType

	recordTypeA     = checkconfig.RecordTypeA
	recordTypeAAAA  = checkconfig.RecordTypeAAAA
	recordTypeCNAME = checkconfig.RecordTypeCNAME
	recordTypeMX    = checkconfig.RecordTypeMX
	recordTypeNS    = checkconfig.RecordTypeNS
	recordTypeTXT   = checkconfig.RecordTypeTXT
	recordTypeSOA   = checkconfig.RecordTypeSOA
)
