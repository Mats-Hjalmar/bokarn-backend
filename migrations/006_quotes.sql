-- A quote is a price the operator has committed to for a short window. It is
-- stored rather than recomputed because the number a guest saw is the number
-- they must be charged, and a recomputation is free to disagree with it.
--
-- input_hash covers everything the guest chose; breakdown_hash covers what the
-- engine produced. Confirming a booking compares the request's input_hash to the
-- stored one, so a changed party or changed dates are refused rather than
-- quietly repriced at the old total.
create table quote (
    tenant_id       uuid not null default current_tenant_id(),
    id              uuid primary key default uuidv7(),
    site_id         uuid not null,
    category_id     uuid not null,
    rate_plan_id    uuid not null,
    arrival         date not null,
    departure       date not null,
    currency        char(3) not null,
    engine_version  integer not null,
    input_hash      bytea not null,
    breakdown_hash  bytea not null,
    -- The full breakdown and explain trace as the engine emitted them. Stored
    -- whole: reconstructing a historical price from rate rules that have since
    -- been edited is exactly what this table exists to avoid.
    payload         jsonb not null,
    total_gross_minor bigint not null,
    total_net_minor   bigint not null,
    total_vat_minor   bigint not null,
    expires_at      timestamptz not null,
    created_at      timestamptz not null default now(),
    unique (tenant_id, id),
    check (departure > arrival),
    check (total_net_minor + total_vat_minor = total_gross_minor),
    foreign key (tenant_id, site_id) references sites (tenant_id, id)
        on delete restrict,
    foreign key (tenant_id, category_id)
        references unit_category (tenant_id, id) on delete restrict,
    foreign key (tenant_id, rate_plan_id)
        references rate_plan (tenant_id, id) on delete restrict
);

create index quote_expiry_idx on quote (tenant_id, expires_at);

select rls_enable('quote', 'bokarn_app');

---- create above / drop below ----

drop table if exists quote;
