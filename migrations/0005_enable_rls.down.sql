revoke select on photos, albums, tracks, posts from anon, authenticated;

drop policy "public read" on photos;
drop policy "public read" on albums;
drop policy "public read" on tracks;
drop policy "public read published" on posts;

alter table photos disable row level security;
alter table albums disable row level security;
alter table tracks disable row level security;
alter table posts disable row level security;
