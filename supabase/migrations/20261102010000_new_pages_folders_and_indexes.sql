-- Only a space's owner may put a page in one of its folders, but
-- pages_guard checked that only when a page moved: anyone who may edit a
-- shared page could make a subpage of it in any folder of the owner's, a
-- folder the page's audience has no part in. A new page now goes in a
-- folder only for the space's owner, as a moved one does. The app makes a
-- subpage in no folder, and sends a page as an upsert, whose insert comes
-- first: a page the server has already goes on to the update, which keeps
-- its folder or checks the change, as before.
--
-- And the foreign keys with no index of their own get one: deleting an
-- account, a folder, a comment, a provider or a job had each of these
-- tables scanned whole, to set null or cascade.

create or replace function public.pages_guard()
returns trigger
language plpgsql
security definer
set search_path = ''
as $$
declare
  v_uid uuid := auth.uid();
  v_parent_space uuid;
begin
  if new.parent_id is not null then
    select p.space_id into v_parent_space from public.pages p where p.id = new.parent_id;
    if v_parent_space is null then
      raise exception 'parent_not_found' using errcode = 'P0002';
    end if;
    if v_parent_space <> new.space_id then
      raise exception 'parent_in_other_space' using errcode = '23514';
    end if;
  end if;

  if new.folder_id is not null and not exists (
    select 1 from public.folders f where f.id = new.folder_id and f.space_id = new.space_id
  ) then
    raise exception 'folder_in_other_space' using errcode = '23514';
  end if;

  if tg_op = 'INSERT' then
    if new.folder_id is not null
       and v_uid is not null
       and not public.is_space_owner(new.space_id)
       and not exists (select 1 from public.pages p where p.id = new.id) then
      raise exception 'forbidden_folder' using errcode = '42501';
    end if;
    if v_uid is not null then
      new.created_by := v_uid;
    end if;
    -- A page made offline keeps its earlier time, as its snapshots do.
    new.placed_at := least(coalesce(new.created_at, now()), now());
    return new;
  end if;

  if new.space_id <> old.space_id then
    raise exception 'space_immutable' using errcode = '23514';
  end if;

  if v_uid is not null then
    new.created_by := old.created_by;
  end if;
  new.placed_at := old.placed_at;

  if new.parent_id is distinct from old.parent_id then
    if new.parent_id is not null and exists (
      with recursive anc as (
        select p.id, p.parent_id, 0 as depth from public.pages p where p.id = new.parent_id
        union all
        select p.id, p.parent_id, a.depth + 1
        from public.pages p join anc a on p.id = a.parent_id
        where a.depth < 64
      )
      select 1 from anc where anc.id = new.id
    ) then
      raise exception 'page_cycle' using errcode = '23514';
    end if;

    if v_uid is not null then
      if new.parent_id is null then
        if not public.is_space_owner(new.space_id) then
          raise exception 'forbidden_move' using errcode = '42501';
        end if;
      elsif not public.can_edit_page(new.parent_id) then
        raise exception 'forbidden_move' using errcode = '42501';
      elsif not public.is_space_owner(new.space_id)
            and public.shared_chain(old.parent_id) <> public.shared_chain(new.parent_id) then
        raise exception 'forbidden_move' using errcode = '42501';
      end if;
    end if;

    new.placed_at := now();
  end if;

  if new.folder_id is distinct from old.folder_id
     and v_uid is not null
     and not public.is_space_owner(new.space_id) then
    raise exception 'forbidden_folder' using errcode = '42501';
  end if;

  return new;
end;
$$;

create index if not exists folders_parent_folder_idx on public.folders (parent_folder_id);
create index if not exists pages_created_by_idx on public.pages (created_by);
create index if not exists page_content_updated_by_idx on public.page_content (updated_by);
create index if not exists page_updates_created_by_idx on public.page_updates (created_by);
create index if not exists page_permissions_invited_by_idx on public.page_permissions (invited_by);
create index if not exists page_email_invites_invited_by_idx on public.page_email_invites (invited_by);
create index if not exists comments_author_idx on public.comments (author_id);
create index if not exists recordings_created_by_idx on public.recordings (created_by);
create index if not exists files_created_by_idx on public.files (created_by);
create index if not exists files_folder_idx on public.files (folder_id);
create index if not exists ai_jobs_comment_idx on public.ai_jobs (comment_id);
create index if not exists ai_jobs_provider_idx on public.ai_jobs (provider_id);
create index if not exists page_history_ai_job_idx on public.page_history (ai_job_id);
create index if not exists page_history_created_by_idx on public.page_history (created_by);
create index if not exists stt_settings_provider_idx on public.stt_settings (provider_id);
