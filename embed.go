// Package tring is the module root. It exists only to embed repo-root assets
// — currently the SQL catalog — so any internal package can read them.
//
// Why it's here: go:embed can't reference a parent directory, so the embed
// directive must live beside the file it embeds. Keeping queries.sql at the
// repo root (the listmonk layout) therefore requires this root-level embed.
// This package must stay dependency-free (it imports nothing but `embed`) so
// internal packages can import it without risking a cycle.
package tring

import _ "embed"

// QueriesSQL is the raw contents of queries.sql, the single named-SQL catalog.
// Parsed into a typed handle by internal/queries.
//
//go:embed queries.sql
var QueriesSQL []byte
