-- images were readable by anyone the space shared any page with, so one
-- shared page let its viewers read every image in the space. An image now
-- lives under the page it is on, images/{space_id}/{page_id}/{image_id}.{ext},
-- and those who can view that page can read it. The owner reads and writes
-- every path in their space (images_objects_owner); an image at an older
-- path, {space_id}/{image_id}.{ext}, is the owner's alone.

create or replace function public.can_view_image_path(p_name text)
returns boolean
language plpgsql
stable
security definer
set search_path = ''
as $$
declare
  v_space text := split_part(p_name, '/', 1);
  v_page text := split_part(p_name, '/', 2);
  v_uuid constant text := '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';
begin
  if v_space !~* v_uuid or v_page !~* v_uuid
     or split_part(p_name, '/', 3) = '' or split_part(p_name, '/', 4) <> '' then
    return false;
  end if;
  return exists (
    select 1 from public.pages p
    where p.id = v_page::uuid and p.space_id = v_space::uuid
  ) and public.can_view_page(v_page::uuid);
end;
$$;

drop policy if exists images_objects_shared_read on storage.objects;
create policy images_objects_shared_read on storage.objects for select to authenticated
  using (bucket_id = 'images' and public.can_view_image_path(name));
