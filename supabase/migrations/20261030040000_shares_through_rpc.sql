-- The owner of a page could still insert and update its page_permissions
-- rows directly, past what share_page checks: an invite to an account that
-- has not confirmed its address, under any invite_id or invited_email, or a
-- row moved to another page or person. Shares are now made and changed
-- only through share_page, as the app does (sharing again changes the
-- role), and accepted through accept_page_invite. The owner still reads
-- and deletes the rows, as unshare_page does.

drop policy if exists page_permissions_insert on public.page_permissions;
drop policy if exists page_permissions_update on public.page_permissions;
revoke insert, update on public.page_permissions from anon, authenticated;
