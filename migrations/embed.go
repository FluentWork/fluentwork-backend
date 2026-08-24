// Package migrations embeds ordered SQL schema files for app-server.
package migrations

import "embed"

// SQL contains the ordered *.sql schema files.
//
//go:embed *.sql
var SQL embed.FS
