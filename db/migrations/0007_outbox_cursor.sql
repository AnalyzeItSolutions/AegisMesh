-- How far the dispatch outbox has published each tenant's event log. Kept in the database so a restart resumes instead of publishing everything again.
create table if not exists outbox_cursor (
  tenant_id uuid primary key,
  last_sequence_id bigint not null check (last_sequence_id >= 0),
  updated_at timestamptz not null default now()
);
alter table outbox_cursor enable row level security;
alter table outbox_cursor force row level security;
create policy tenant_isolation on outbox_cursor using (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid) with check (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
