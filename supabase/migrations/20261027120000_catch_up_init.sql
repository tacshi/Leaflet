-- What the init migration gained after projects could have applied it,
-- before the first release: page_content's revision and is_template, and
-- reading files on shared pages. A project made from a release has all of
-- it, so every statement here leaves such a project as it is.

alter table public.pages add column if not exists is_template boolean not null default false;

alter table public.page_content add column if not exists revision bigint not null default 0;

-- page_content's revision moves on with every update, whatever the writer
-- sent, so a writer that does not check it still makes the others retry.
create or replace function public.bump_revision()
returns trigger
language plpgsql
set search_path = ''
as $$
begin
  new.revision := old.revision + 1;
  return new;
end;
$$;

drop trigger if exists page_content_bump_revision on public.page_content;
create trigger page_content_bump_revision before update on public.page_content
  for each row execute function public.bump_revision();

create index if not exists files_page_idx on public.files (page_id);
create index if not exists files_storage_path_idx on public.files (storage_path);

-- Anyone who can view the page a file is on can read its row; reading its
-- object is files_objects_shared_read, from 20261027000000.
drop policy if exists files_select_shared on public.files;
create policy files_select_shared on public.files for select to authenticated
  using (page_id is not null and public.can_view_page(page_id));
