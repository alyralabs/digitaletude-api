create table albums (
  id uuid primary key default gen_random_uuid(),
  title text not null,
  description text not null default '',
  cover_image_path text,
  sort_order int not null default 0,
  created_at timestamptz not null default now(),
  metadata jsonb not null default '{}'
);

create table tracks (
  id uuid primary key default gen_random_uuid(),
  title text not null,
  description text not null default '',
  storage_path text not null,
  cover_image_path text,
  duration_seconds int,
  album_id uuid references albums(id) on delete set null,
  track_number int,
  sort_order int not null default 0,
  created_at timestamptz not null default now(),
  metadata jsonb not null default '{}'
);

create index tracks_order_idx on tracks (sort_order, created_at desc);
create index tracks_album_idx on tracks (album_id, track_number);
