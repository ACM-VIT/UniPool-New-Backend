// Package migsource owns the embedded goose migration files.
package migsource

import "embed"

// FS is the read-only filesystem view of every SQL migration.
//
//go:embed migrations/*.sql
var FS embed.FS
