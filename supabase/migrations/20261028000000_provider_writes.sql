-- The server calls a model provider's base_url, so a row must not be
-- written past the providers function's checks (https, a public host, local
-- presets never called). Clients save providers through that function, which
-- writes with the service role; they keep reading and deleting their rows,
-- and setting keys through set_provider_secret.
--
-- Revoking the table privileges also revokes the column grants from init.

revoke insert, update on public.model_providers from authenticated;
