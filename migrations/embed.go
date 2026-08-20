// Package migrations exposes the project's tern SQL migration files as an
// embedded filesystem. The files are compiled into the binary so the `migrate`
// subcommand (and the e2e suite) apply exactly the migrations that were built,
// with no dependency on the filesystem layout at runtime.
package migrations

import "embed"

// FS holds the embedded tern SQL migration files.
//
//go:embed *.sql
var FS embed.FS
