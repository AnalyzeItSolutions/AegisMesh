create table if not exists units (
  id uuid primary key default gen_random_uuid(),
  callsign text,
  capacity integer,
  location geography(Point, 4326),
  status text,
  tenant_id uuid not null references tenants(id),
  created_at timestamptz not null default now()
);
create index if not exists units_location_gist on units using gist (location);
create index if not exists units_tenant_idx on units (tenant_id);
alter table units enable row level security;
alter table units force row level security;
create policy tenant_isolation on units using (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid) with check (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
