-- What an operator has to sell. This stops at availability: what is free on a
-- given night depends on allocations, which live in 004_occupancy.sql.
--
-- The division between a first-class column and an amenity tag is deliberate
-- and load-bearing. Anything the availability filter tests or the assigner
-- scores on is a column, because `electricity_amp >= 10` and
-- `max_vehicle_length_m >= 9` are range tests that neither a jsonb blob nor a
-- tag table can serve from an index. Everything else is a tag nobody filters.

create table unit_category (
    tenant_id            uuid not null default current_tenant_id(),
    id                   uuid primary key default uuidv7(),
    site_id              uuid not null,
    code                 text not null,
    name                 text not null,
    kind                 text not null check (kind in
                             ('tomt', 'stuga', 'villavagn', 'glamping', 'rum')),
    revenue_class        text not null check (revenue_class in
                             ('pitch', 'lodging')),
    max_occupancy        smallint not null check (max_occupancy > 0),
    min_electricity_amp  smallint,
    pets_allowed         boolean not null default true,
    accessible           boolean not null default false,
    sanitary             boolean not null default false,
    sort_order           integer not null default 0,
    created_at           timestamptz not null default now(),
    updated_at           timestamptz not null default now(),
    unique (tenant_id, id),
    -- code is what rate plans, channels and imports map to. Renaming one
    -- silently remaps inventory, so it is a natural key rather than a label.
    unique (tenant_id, site_id, code),
    foreign key (tenant_id, site_id) references sites (tenant_id, id)
        on delete restrict
);

create index unit_category_site_idx on unit_category (tenant_id, site_id);

create table unit (
    tenant_id            uuid not null default current_tenant_id(),
    id                   uuid primary key default uuidv7(),
    site_id              uuid not null,
    category_id          uuid not null,
    code                 text not null,
    status               text not null default 'active'
                             check (status in ('active', 'retired')),

    electricity_amp      smallint,
    area_m2              smallint,
    max_vehicle_length_m numeric(4, 1),
    max_occupancy        smallint not null check (max_occupancy > 0),
    pets_allowed         boolean not null default true,
    accessible           boolean not null default false,

    -- A water tap, a grey-water dump and a full sewer connection are three
    -- different things to a motorhome owner and are commonly priced apart.
    has_water            boolean not null default false,
    has_greywater        boolean not null default false,
    has_sewer            boolean not null default false,

    surface              text check (surface in
                             ('grass', 'gravel', 'hardstanding', 'sand', 'other')),
    shade                text check (shade in ('sun', 'partial', 'shade')),
    -- Whether a large rig can drive straight through without reversing. A hard
    -- constraint for big vehicles, not a preference.
    drive_through        boolean not null default false,

    sanitary             boolean not null default false,
    view                 text,
    cleanliness          text not null default 'ready'
                             check (cleanliness in ('clean', 'dirty', 'ready')),
    map_x                numeric(8, 2),
    map_y                numeric(8, 2),
    sort_order           integer not null default 0,
    created_at           timestamptz not null default now(),
    updated_at           timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, site_id, code),
    foreign key (tenant_id, site_id) references sites (tenant_id, id)
        on delete restrict,
    foreign key (tenant_id, category_id)
        references unit_category (tenant_id, id) on delete restrict
);

-- Availability and the assigner both start from "units of this category that
-- are still in service", so that is the index.
create index unit_category_active_idx on unit (tenant_id, category_id)
    where status = 'active';

create table unit_amenity (
    tenant_id    uuid not null default current_tenant_id(),
    unit_id      uuid not null,
    amenity_code text not null,
    created_at   timestamptz not null default now(),
    primary key (tenant_id, unit_id, amenity_code),
    foreign key (tenant_id, unit_id) references unit (tenant_id, id)
        on delete cascade
);

-- When a unit is open for business. A single open_from/open_to pair would be
-- wrong: a campsite routinely runs May to September and then opens a handful of
-- stugor again over Christmas, which is two disjoint periods on one unit.
create table unit_season (
    tenant_id  uuid not null default current_tenant_id(),
    id         uuid primary key default uuidv7(),
    unit_id    uuid not null,
    period     daterange not null check (not isempty(period)),
    created_at timestamptz not null default now(),
    unique (tenant_id, id),
    foreign key (tenant_id, unit_id) references unit (tenant_id, id)
        on delete cascade,
    exclude using gist (tenant_id with =, unit_id with =, period with &&)
);

create index unit_season_unit_idx on unit_season (tenant_id, unit_id);

create trigger set_updated_at before update on unit_category
    for each row execute function set_updated_at();
create trigger set_updated_at before update on unit
    for each row execute function set_updated_at();

select rls_enable('unit_category', 'bokarn_app');
select rls_enable('unit', 'bokarn_app');
select rls_enable('unit_amenity', 'bokarn_app');
select rls_enable('unit_season', 'bokarn_app');

---- create above / drop below ----

drop table if exists unit_season;
drop table if exists unit_amenity;
drop table if exists unit;
drop table if exists unit_category;
