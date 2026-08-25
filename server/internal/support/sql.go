package support

import "database/sql"

// errNoRows is the sentinel bun returns from Scan when a query matched nothing.
// Aliased here so the callers read as "no live thread" rather than importing
// database/sql for one symbol apiece.
var errNoRows = sql.ErrNoRows
