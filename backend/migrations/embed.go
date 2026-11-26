package migrations

import "embed"

// FS embeds all goose SQL migration files for self-contained execution.
//
//go:embed *.sql
var FS embed.FS
