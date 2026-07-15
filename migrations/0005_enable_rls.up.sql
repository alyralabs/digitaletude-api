-- The frontend now reads Supabase's PostgREST API directly with the anon
-- key, so the tables need RLS with public-read policies. cmd/admin connects
-- as the table owner (postgres role via the pooler), which is exempt from
-- RLS, so writes are unaffected.

alter table photos enable row level security;
alter table albums enable row level security;
alter table tracks enable row level security;
alter table posts enable row level security;

-- RLS policies filter rows, but the roles also need table-level SELECT
-- privilege (verified missing on these tables: without the grant, anon gets
-- "permission denied", not an empty result).
grant usage on schema public to anon, authenticated;
grant select on photos, albums, tracks, posts to anon, authenticated;

create policy "public read" on photos
  for select to anon, authenticated using (true);

create policy "public read" on albums
  for select to anon, authenticated using (true);

create policy "public read" on tracks
  for select to anon, authenticated using (true);

-- Drafts stay invisible to the public; only published posts are readable.
create policy "public read published" on posts
  for select to anon, authenticated using (status = 'published');
