-- The correctness core. One table carries bookings, holds and maintenance
-- blocks, and one constraint guarantees none of them can ever overlap on a
-- physical unit.
--
-- Sharing a shape across all three is the single biggest simplification
-- available: a maintenance block literally cannot collide with a booking,
-- because the same exclusion constraint covers both. There is no second code
-- path to keep in step and no reconciliation job.
--
-- THIS CONSTRAINT HAS NO ONLINE-ADD PATH. Postgres allows neither NOT VALID nor
-- USING INDEX for EXCLUDE, so adding it to a populated table means an ACCESS
-- EXCLUSIVE lock and an inline GiST build. It lands now, while the table is
-- empty, even though only blocks can be written until bookings arrive.

create table unit_allocation (
    tenant_id      uuid not null default current_tenant_id(),
    id             uuid primary key default uuidv7(),
    site_id        uuid not null,
    category_id    uuid not null,

    -- Nullable from the start. v1 always assigns a physical unit when a hold is
    -- taken, but the predicate below already reads `unit_id is not null`, so
    -- selling at category grain later is new code against an unchanged
    -- constraint rather than a migration.
    unit_id        uuid,
    -- Gains its composite foreign key in M4 alongside the booking table. Until
    -- then the CHECK below means only blocks exist and this is always null.
    booking_id     uuid,

    kind           text not null check (kind in ('booking', 'hold', 'block')),
    state          text not null check (state in
                       ('held', 'confirmed', 'checked_in', 'checked_out',
                        'cancelled', 'expired', 'no_show')),

    -- daterange canonicalises to [), so same-day turnover is free and
    -- [07-01,07-05) provably does not overlap [07-05,07-08). The upper bound is
    -- the departure date, never "last night". date rather than timestamptz
    -- because a night is a calendar date in the site's timezone, which
    -- eliminates every daylight-saving bug at the type level.
    stay           daterange not null check (not isempty(stay)),
    arrival_date   date generated always as (lower(stay)) stored,
    departure_date date generated always as (upper(stay)) stored,

    unit_pinned    boolean not null default false,
    expires_at     timestamptz,
    block_reason   text,
    adults         smallint not null default 0 check (adults >= 0),
    children       smallint not null default 0 check (children >= 0),
    pets           smallint not null default 0 check (pets >= 0),
    created_at     timestamptz not null default now(),
    updated_at     timestamptz not null default now(),

    unique (tenant_id, id),
    check ((kind = 'block') = (booking_id is null)),
    check ((kind = 'hold') = (expires_at is not null)),
    check (not (unit_pinned and unit_id is null)),

    foreign key (tenant_id, site_id) references sites (tenant_id, id)
        on delete restrict,
    foreign key (tenant_id, category_id)
        references unit_category (tenant_id, id) on delete restrict,
    foreign key (tenant_id, unit_id) references unit (tenant_id, id)
        on delete restrict,

    -- Not deferrable: an immediate 23P01 is what lets the first-fit assignment
    -- loop try the next candidate unit instead of failing the request.
    --
    -- tenant_id with = keeps hash partitioning by tenant possible later (a &&
    -- operator would not qualify as a partition key) and guarantees the error
    -- message can never name another operator's row.
    --
    -- Index predicates must be IMMUTABLE, so now() cannot appear here. That is
    -- precisely why hold expiry is an explicit state write and never a
    -- time-based filter.
    -- Named rather than left to Postgres: the name reaches the client in the
    -- 23P01 message and the assignment loop keys its retry on it.
    constraint unit_allocation_no_overlap
        exclude using gist (tenant_id with =, unit_id with =, stay with &&)
        where (unit_id is not null
               and state in ('held', 'confirmed', 'checked_in', 'checked_out'))
);

alter table unit_allocation alter column tenant_id set statistics 1000;

create index unit_allocation_arrivals_idx
    on unit_allocation (tenant_id, site_id, arrival_date)
    where state in ('confirmed', 'checked_in');

create index unit_allocation_departures_idx
    on unit_allocation (tenant_id, site_id, departure_date)
    where state = 'checked_in';

create index unit_allocation_expiring_idx
    on unit_allocation (tenant_id, expires_at)
    where state = 'held';

create index unit_allocation_unassigned_idx
    on unit_allocation (tenant_id, site_id, category_id, arrival_date)
    where unit_id is null;

-- The tape chart reads one unit's rows across a date window.
create index unit_allocation_unit_stay_idx
    on unit_allocation using gist (tenant_id, unit_id, stay);

create trigger set_updated_at before update on unit_allocation
    for each row execute function set_updated_at();

select rls_enable('unit_allocation', 'bokarn_app');

---- create above / drop below ----

drop table if exists unit_allocation;
