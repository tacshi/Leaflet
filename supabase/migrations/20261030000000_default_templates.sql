-- A folder's new pages, and a page's new subpages, can start from a
-- template: default_template_id names it, or is null for blank pages. It
-- rides the row like its name or title. No foreign key: the template may be
-- one only the owner has, or still on its way up from a device, and a
-- missing one just leaves new pages blank.

alter table public.folders add column if not exists default_template_id uuid;
alter table public.pages add column if not exists default_template_id uuid;
