// Package migrations embeds the SQL schema files into the binary.
//
// Embedding rather than reading from disk means a deployed binary carries the
// exact schema it was built against. A container that shipped without its
// migrations directory, or with a stale copy of it, is a class of production
// incident this removes entirely.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS

// Dir is the path within FS. A constant so the migrator has no string literal
// to get wrong.
const Dir = "."
