-- Pages carry tags: words a person files a page under, shown as chips in
-- its byline and grouped in the sidebar's Tags tab. They ride the pages row
-- like its title, so sharing a page shares its tags. The app tidies them
-- (no leading #, no repeats in any case); the server keeps them in bounds.

alter table public.pages add column if not exists tags text[] not null default '{}';

create or replace function public.tags_in_bounds(p_tags text[])
returns boolean
language sql
immutable
set search_path = ''
as $$
  select coalesce(array_length(p_tags, 1), 0) <= 50
    and not exists (
      select 1 from unnest(p_tags) t
      where t is null or char_length(t) = 0 or char_length(t) > 64
    );
$$;

alter table public.pages drop constraint if exists pages_tags_in_bounds;
alter table public.pages add constraint pages_tags_in_bounds check (public.tags_in_bounds(tags));
