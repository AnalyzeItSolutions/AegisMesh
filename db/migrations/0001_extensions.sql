create extension if not exists pgcrypto;
create extension if not exists postgis;
create extension if not exists vector;

create table if not exists tenants (id uuid primary key default gen_random_uuid(), name text not null, created_at timestamptz not null default now());
