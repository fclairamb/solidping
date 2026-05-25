package discovery

// portSpec is one entry in the authoritative port table. It is the single
// source of truth shared by the scanner (which ports to probe) and the
// suggestion engine (how to map an open port to a suggested check). Keeping
// both consumers driven by this one slice removes the drift hazard that
// existed when the scanner's port list and the suggestion switch were
// maintained independently.
type portSpec struct {
	Port      int
	CheckType string // "http" or "tcp"
	URLTmpl   string // only for http (e.g. "http://%s"); empty for tcp
}

// defaultPorts is the authoritative set of ports probed by the scanner and
// understood by the suggestion engine. Adding a port here automatically makes
// the scanner probe it and the suggestion engine emit a check for it.
//
//nolint:gochecknoglobals // authoritative port table; treated as a constant lookup list
var defaultPorts = []portSpec{
	{22, checkTypeTCP, ""},
	{25, checkTypeTCP, ""},
	{53, checkTypeTCP, ""},
	{80, checkTypeHTTP, "http://%s"},
	{110, checkTypeTCP, ""},
	{143, checkTypeTCP, ""},
	{443, checkTypeHTTP, "https://%s"},
	{465, checkTypeTCP, ""},
	{587, checkTypeTCP, ""},
	{993, checkTypeTCP, ""},
	{995, checkTypeTCP, ""},
	{3306, checkTypeTCP, ""},
	{5432, checkTypeTCP, ""},
	{6379, checkTypeTCP, ""},
	{8080, checkTypeTCP, ""},
	{8443, checkTypeTCP, ""},
}

// defaultPortList returns the default set of ports to scan when none are
// specified, derived from the authoritative defaultPorts table.
func defaultPortList() []int {
	ports := make([]int, len(defaultPorts))
	for i := range defaultPorts {
		ports[i] = defaultPorts[i].Port
	}

	return ports
}
