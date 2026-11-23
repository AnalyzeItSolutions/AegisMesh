create table if not exists incidents (
  id uuid primary key default gen_random_uuid(),
  kind text,
  severity text,
  location geography(Point, 4326),
  h3_cell bigint,
  embedding vector(384),
  status text,
  reported_at timestamptz,
  tenant_id uuid not null references tenants(id),
  created_at timestamptz not null default now()
);
create index if not exists incidents_location_gist on incidents using gist (location);
create index if not exists incidents_h3_cell_idx on incidents (h3_cell);
create index if not exists incidents_embedding_hnsw on incidents using hnsw (embedding vector_cosine_ops);
create index if not exists incidents_tenant_idx on incidents (tenant_id);
alter table incidents enable row level security;
alter table incidents force row level security;
create policy tenant_isolation on incidents using (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid) with check (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
