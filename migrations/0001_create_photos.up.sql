create table photos (
  id uuid primary key default gen_random_uuid(),
  title text not null,
  description text not null default '',
  storage_path text not null,
  thumbnail_path text not null,
  width int not null,
  height int not null,
  sort_order int not null default 0,
  created_at timestamptz not null default now()
);

create index photos_order_idx on photos (sort_order, created_at desc);
