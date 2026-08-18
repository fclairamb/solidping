// Package gomigrations holds the SQLite migrations that cannot be expressed as
// plain SQL.
//
// bun derives a Go migration's name and comment from the FILE NAME of whatever
// calls Migrations.Register, exactly as it does for `NNN_vX_Y_Z.up.sql`. Each
// file in this package is therefore named after the migration it registers, and
// registers exactly one.
package gomigrations
