// Package migsource owns the embedded goose migration files.
//
// Goose embed directives can't traverse `..`, so the migrations
// directory has to live as a sibling of the file holding the
// //go:embed line. Keeping that file in its own package (rather than
// in main) lets both the prod binary and the integration test
// harness pull from the same canonical source.
package migsource

import "embed"

// FS is the read-only filesystem view of every .sql migration. Pass
// it directly to database.RunMigrations or to goose.SetBaseFS in
// tests. The directory layout inside is `migrations/0000N_name.sql`,
// matching the relative path used in goose.Up("migrations").
//
//go:embed migrations/*.sql
var FS embed.FS
