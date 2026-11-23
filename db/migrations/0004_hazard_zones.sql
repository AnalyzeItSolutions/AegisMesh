create table if not exists hazard_zones (
  id uuid primary key default gen_random_uuid(),
  polygon geography(Point, 4326),
  kind text,
  active boolean,
  tenant_id uuid not null references tenants(id),
  created_at timestamptz not null default now()
);
create index if not exists hazard_zones_polygon_gist on hazard_zones using gist (polygon);
create index if not exists hazard_zones_tenant_idx on hazard_zones (tenant_id);
alter table hazard_zones enable row level security;
alter table hazard_zones force row level security;
create policy tenant_isolation on hazard_zones using (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid) with check (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
