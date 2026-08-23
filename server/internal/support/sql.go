package support

import "database/sql"

// sqlNoRows is the sentinel bun returns from Scan when a query matched nothing.
// Aliased here so the callers read as "no live thread" rather than importing
// database/sql for one symbol apiece.
var sqlNoRows = sql.ErrNoRows
