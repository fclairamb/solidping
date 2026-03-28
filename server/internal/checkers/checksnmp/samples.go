package checksnmp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// GetSampleConfigs returns sample SNMP check configurations.
func (c *SNMPChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "SNMP sysDescr",
			Slug:   "snmp-sysdescr",
			Period: 5 * time.Minute,
			Config: (&SNMPConfig{
				Host: "localhost",
				OID:  ".1.3.6.1.2.1.1.1.0",
			}).GetConfig(),
		},
	}
}
