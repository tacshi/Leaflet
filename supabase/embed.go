// Package supabase carries the backend, so the app can set up a person's
// own Supabase project: its migrations and Edge Functions.
package supabase

import "embed"

// Backend holds migrations/*.sql and the functions' sources, at their paths
// in this folder: each function's folder, and the modules they share. The
// tests' own helpers, in functions/_testing, stay out.
//
//go:embed migrations/*.sql functions/_shared/*.ts functions/agent/*.ts functions/providers/*.ts
//go:embed functions/stt/*.ts functions/account/*.ts
var Backend embed.FS
