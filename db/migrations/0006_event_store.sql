create table if not exists event_store (
  sequence_id bigint not null,
  tenant_id uuid not null,
  aggregate_id uuid not null,
  event_type text not null,
  payload jsonb not null,
  actor_id text not null,
  prev_hash char(64) not null,
  entry_hash char(64) not null unique,
  created_at timestamptz not null default now(),
  primary key (tenant_id, sequence_id)
);
create index if not exists event_store_aggregate_idx on event_store (tenant_id, aggregate_id, sequence_id);
create or replace function event_store_immutable() returns trigger language plpgsql as $$ begin raise exception 'event_store is append-only'; end $$;
create trigger event_store_no_update before update or delete on event_store for each row execute function event_store_immutable();
alter table event_store enable row level security;
alter table event_store force row level security;
create policy tenant_isolation on event_store using (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid) with check (tenant_id = nullif(current_setting('app.tenant_id', true), '')::uuid);
