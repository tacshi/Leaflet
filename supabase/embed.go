package supabase

import "embed"

//go:embed migrations/*.sql functions/_shared/*.ts functions/account/*.ts
var Backend embed.FS
