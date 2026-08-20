-- Pricing. The central idea is two surfaces: rate_season is what staff author,
-- rate_day is what the engine reads — one row per plan per date, compiled at
-- write time with the winning season recorded.
--
-- Evaluating overlapping seasons at query time was rejected. It is
-- non-deterministic while someone is editing, it is slow exactly where speed
-- matters, and it leaves "why is 2027-07-04 priced at 495?" with no answer that
-- survives the next edit.

-- A date-effective lookup, never a constant in code. The Swedish food rate moved
-- to 6% for 2026-2027 and Finland's accommodation rate moved to 13.5% in 2026;
-- a rate baked into a product row would silently reprice history.
--
-- vat_treatment is separate from the rate because 0 bp alone cannot distinguish
-- a cancellation fee (outside the scope of VAT) from an exempt supply, and the
-- two appear differently on a VAT return.
create table vat_codes (
    tenant_id      uuid not null default current_tenant_id(),
    code           text not null,
    country        char(2) not null references countries (code),
    rate_bp        integer not null check (rate_bp >= 0 and rate_bp <= 10000),
    vat_treatment  text not null check (vat_treatment in
                       ('standard', 'zero_rated', 'outside_scope',
                        'reverse_charge')),
    valid_from     date not null,
    valid_to       date,
    account_code   text,
    created_at     timestamptz not null default now(),
    primary key (tenant_id, code, valid_from),
    check (valid_to is null or valid_to > valid_from)
);

create table cancellation_policy (
    tenant_id  uuid not null default current_tenant_id(),
    id         uuid primary key default uuidv7(),
    name       text not null,
    created_at timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, name)
);

-- The ladder is data, not code, and a snapshot of it is frozen onto every
-- booking at creation so a later edit cannot change what a guest already owes.
create table cancellation_band (
    tenant_id        uuid not null default current_tenant_id(),
    policy_id        uuid not null,
    days_before_min  integer not null,
    days_before_max  integer,
    charge_pct       integer not null check (charge_pct between 0 and 100),
    fixed_fee_minor  bigint not null default 0,
    primary key (tenant_id, policy_id, days_before_min),
    foreign key (tenant_id, policy_id)
        references cancellation_policy (tenant_id, id) on delete cascade
);

create table rate_plan (
    tenant_id             uuid not null default current_tenant_id(),
    id                    uuid primary key default uuidv7(),
    category_id           uuid not null,
    code                  text not null,
    name                  text not null,
    currency              char(3) not null references currencies (code),
    vat_code              text not null,
    -- A derived plan is priced from its parent, which is how "non-refundable,
    -- 10% off" stays in step with the standard rate instead of drifting.
    parent_id             uuid,
    derive_op             text check (derive_op in ('percent', 'amount')),
    derive_value_bp       integer,
    -- Bounds exist to clamp the dynamic adjusters below; without them an
    -- occupancy rule can price a pitch at anything.
    min_price_minor       bigint,
    max_price_minor       bigint,
    refundable            boolean not null default true,
    cancellation_policy_id uuid,
    priority              integer not null default 0,
    is_active             boolean not null default true,
    created_at            timestamptz not null default now(),
    updated_at            timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, code),
    check ((parent_id is null) = (derive_op is null)),
    foreign key (tenant_id, category_id)
        references unit_category (tenant_id, id) on delete restrict,
    foreign key (tenant_id, parent_id)
        references rate_plan (tenant_id, id) on delete restrict,
    foreign key (tenant_id, cancellation_policy_id)
        references cancellation_policy (tenant_id, id) on delete restrict
);

create index rate_plan_category_idx on rate_plan (tenant_id, category_id)
    where is_active;

-- The authoring surface: broad strokes staff actually think in, "high season is
-- 495 a night, Saturdays only for cabins".
--
-- weekday_mask and arrival_mask are bitmaps, Monday = 1. arrival_mask is not
-- decoration: Nordic cabins are overwhelmingly sold Saturday to Saturday, and a
-- system that cannot express that cannot sell them.
create table rate_season (
    tenant_id            uuid not null default current_tenant_id(),
    id                   uuid primary key default uuidv7(),
    rate_plan_id         uuid not null,
    name                 text not null,
    starts_on            date not null,
    ends_on              date not null,
    weekday_mask         smallint not null default 127,
    priority             integer not null default 0,
    base_minor           bigint not null check (base_minor >= 0),
    included_adults      smallint not null default 2,
    included_children    smallint not null default 0,
    adult_extra_minor    bigint not null default 0,
    child_extra_minor    bigint not null default 0,
    pet_minor            bigint not null default 0,
    vehicle_minor        bigint not null default 0,
    min_stay             smallint not null default 1 check (min_stay >= 1),
    max_stay             smallint,
    arrival_mask         smallint not null default 127,
    closed               boolean not null default false,
    closed_to_arrival    boolean not null default false,
    closed_to_departure  boolean not null default false,
    created_at           timestamptz not null default now(),
    updated_at           timestamptz not null default now(),
    unique (tenant_id, id),
    check (ends_on >= starts_on),
    check (max_stay is null or max_stay >= min_stay),
    foreign key (tenant_id, rate_plan_id)
        references rate_plan (tenant_id, id) on delete cascade
);

create index rate_season_plan_idx on rate_season (tenant_id, rate_plan_id);

-- The evaluation surface, compiled from rate_season. One row per plan per date.
-- source_season_id is the whole point: overlap resolution happens once, at write
-- time, and the winner is recorded, so the price of any date has a stored
-- explanation rather than one re-derived on demand.
create table rate_day (
    tenant_id            uuid not null default current_tenant_id(),
    rate_plan_id         uuid not null,
    day                  date not null,
    currency             char(3) not null,
    base_minor           bigint not null,
    included_adults      smallint not null,
    included_children    smallint not null,
    adult_extra_minor    bigint not null,
    child_extra_minor    bigint not null,
    pet_minor            bigint not null,
    vehicle_minor        bigint not null,
    min_stay             smallint not null,
    max_stay             smallint,
    arrival_mask         smallint not null,
    closed               boolean not null,
    closed_to_arrival    boolean not null,
    closed_to_departure  boolean not null,
    source_season_id     uuid not null,
    compiled_at          timestamptz not null default now(),
    primary key (tenant_id, rate_plan_id, day),
    foreign key (tenant_id, rate_plan_id)
        references rate_plan (tenant_id, id) on delete cascade,
    foreign key (tenant_id, source_season_id)
        references rate_season (tenant_id, id) on delete cascade
);

-- Nordic sites price 0-3, 4-12 and 13-15 differently, so a child needs a date of
-- birth rather than a checkbox. Without these a fifteen-year-old cannot be
-- classified at all.
create table rate_age_band (
    tenant_id            uuid not null default current_tenant_id(),
    rate_plan_id         uuid not null,
    code                 text not null,
    age_from             smallint not null check (age_from >= 0),
    age_to               smallint not null,
    price_per_night_minor bigint not null,
    primary key (tenant_id, rate_plan_id, code),
    check (age_to >= age_from),
    foreign key (tenant_id, rate_plan_id)
        references rate_plan (tenant_id, id) on delete cascade
);

-- Occupancy and lead-time pricing. trigger is jsonb because the shape of a rule
-- is genuinely open-ended; the engine reads only the keys it documents and
-- ignores nothing silently — an unknown key is a compile error, not a no-op.
create table pricing_adjuster (
    tenant_id     uuid not null default current_tenant_id(),
    id            uuid primary key default uuidv7(),
    category_id   uuid not null,
    name          text not null,
    trigger       jsonb not null,
    factor_bp     integer,
    delta_minor   bigint,
    priority      integer not null default 0,
    enabled       boolean not null default true,
    created_at    timestamptz not null default now(),
    unique (tenant_id, id),
    check (num_nonnulls(factor_bp, delta_minor) = 1),
    foreign key (tenant_id, category_id)
        references unit_category (tenant_id, id) on delete cascade
);

create index pricing_adjuster_category_idx
    on pricing_adjuster (tenant_id, category_id) where enabled;

-- Everything sold that is not a night: electricity, linen, firewood, the
-- booking fee. statistics_bucket is here because Tillväxtverket wants lodging
-- revenue separated from everything else, and deciding that per sale later is
-- guesswork.
create table product (
    tenant_id         uuid not null default current_tenant_id(),
    id                uuid primary key default uuidv7(),
    sku               text not null,
    name              text not null,
    vat_code          text not null,
    basis             text not null check (basis in
                          ('per_stay', 'per_night', 'per_person',
                           'per_person_per_night', 'per_unit',
                           'per_unit_per_night', 'metered')),
    revenue_group     text not null,
    revenue_account   text,
    statistics_bucket text not null default 'out_of_scope'
                          check (statistics_bucket in
                              ('lodging_regular', 'lodging_seasonal',
                               'out_of_scope')),
    is_active         boolean not null default true,
    created_at        timestamptz not null default now(),
    updated_at        timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, sku)
);

create table product_price (
    tenant_id    uuid not null default current_tenant_id(),
    product_id   uuid not null,
    valid_from   date not null,
    valid_to     date,
    price_minor  bigint not null,
    currency     char(3) not null references currencies (code),
    primary key (tenant_id, product_id, valid_from),
    check (valid_to is null or valid_to > valid_from),
    foreign key (tenant_id, product_id)
        references product (tenant_id, id) on delete cascade
);

create table campaign (
    tenant_id       uuid not null default current_tenant_id(),
    id              uuid primary key default uuidv7(),
    code            text not null,
    name            text not null,
    kind            text not null check (kind in ('percent', 'amount')),
    value           bigint not null check (value > 0),
    stackable       boolean not null default false,
    priority        integer not null default 0,
    max_redemptions integer,
    book_from       date,
    book_to         date,
    stay_from       date,
    stay_to         date,
    is_active       boolean not null default true,
    created_at      timestamptz not null default now(),
    unique (tenant_id, id)
);

-- Case-insensitive so SOMMAR10 and sommar10 are one campaign. An expression
-- cannot appear in a UNIQUE constraint, only in a unique index.
create unique index campaign_code_idx on campaign (tenant_id, upper(code));

create trigger set_updated_at before update on rate_plan
    for each row execute function set_updated_at();
create trigger set_updated_at before update on rate_season
    for each row execute function set_updated_at();
create trigger set_updated_at before update on product
    for each row execute function set_updated_at();

select rls_enable('vat_codes', 'bokarn_app');
select rls_enable('cancellation_policy', 'bokarn_app');
select rls_enable('cancellation_band', 'bokarn_app');
select rls_enable('rate_plan', 'bokarn_app');
select rls_enable('rate_season', 'bokarn_app');
select rls_enable('rate_day', 'bokarn_app');
select rls_enable('rate_age_band', 'bokarn_app');
select rls_enable('pricing_adjuster', 'bokarn_app');
select rls_enable('product', 'bokarn_app');
select rls_enable('product_price', 'bokarn_app');
select rls_enable('campaign', 'bokarn_app');

---- create above / drop below ----

drop table if exists campaign;
drop table if exists product_price;
drop table if exists product;
drop table if exists pricing_adjuster;
drop table if exists rate_age_band;
drop table if exists rate_day;
drop table if exists rate_season;
drop table if exists rate_plan;
drop table if exists cancellation_band;
drop table if exists cancellation_policy;
drop table if exists vat_codes;
