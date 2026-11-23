create table if not exists tenants (
  id uuid primary key default gen_random_uuid(),
  name text,
  created_at timestamptz
);
