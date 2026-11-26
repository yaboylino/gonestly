// Package migrations holds the SQL migrations, embedded in the binary.
package migrations

import "embed"

// Files are applied in order when the application starts.
//
//go:embed *.sql
var Files embed.FS
