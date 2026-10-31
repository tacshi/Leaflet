// Package supabase carries the backend, so the app can set up a person's
// own Supabase project: its migrations and Edge Functions.
package supabase

import "embed"

// Backend holds migrations/*.sql and the functions' sources, at their paths
// in this folder.
//
//go:embed migrations/*.sql functions/*/index.ts functions/_shared/*.ts
var Backend embed.FS
