// Package migrations embeds the golang-migrate SQL files of this service,
// so the binary carries its schema and the image needs no files on disk.
// The files are additive: a change is a new numbered pair, an existing
// migration is never edited, and every down recreates the previous schema
// exactly.
package migrations

import "embed"

// FS holds the NNNN_name.up.sql / NNNN_name.down.sql files at its root.
//
//go:embed *.sql
var FS embed.FS
