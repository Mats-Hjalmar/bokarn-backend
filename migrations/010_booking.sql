-- The booking itself, and the frozen record of what was agreed.
--
-- The organising decision here is that a booking has no state column. Occupancy
-- state lives on unit_allocation and nowhere else, because two state machines
-- describing one stay will disagree, and the one the exclusion constraint reads
-- is the one that decides whether a pitch is double-booked. A booking is the
-- contract; the allocation is the occupancy; reads join them.
--
-- Everything the guest agreed to is copied, not referenced: the price lines, the
-- cancellation terms, the engine version. Rate rules are edited constantly and a
-- price reconstructed from today's rules is free to disagree with the number the
-- guest saw. That is the whole point of the freeze.

create table booking (
    tenant_id           uuid not null default current_tenant_id(),
    id                  uuid primary key default uuidv7(),

    -- What the guest quotes on the phone. Not a secret and not a credential:
    -- booking_access_token below is what authorises reading the booking.
    reference           text not null check (reference <> ''),

    site_id             uuid not null,
    category_id         uuid not null,
    guest_id            uuid not null,

    quote_id            uuid not null,
    engine_version      integer not null,
    -- The two hashes the freeze rests on. input_hash is compared against the
    -- confirm request so a changed party or changed dates are refused rather
    -- than quietly repriced; quote_hash records which breakdown was accepted.
    input_hash          bytea not null,
    quote_hash          bytea not null,

    -- A guest double-tapping "Boka" on campsite 4G is the real duplicate
    -- booking failure mode, and no amount of provider-side idempotency helps
    -- with it. Required on the request, unique here, so the replay returns the
    -- same reference instead of a second stay.
    idempotency_key     text not null,

    -- The cancellation ladder as it stood at confirmation. Null when the rate
    -- plan named no policy, which is not "free cancellation": the cancel path
    -- must refuse to compute a fee it was never given terms for.
    cancellation_policy jsonb,

    currency            char(3) not null references currencies (code),
    total_gross_minor   bigint not null,
    total_net_minor     bigint not null,
    total_vat_minor     bigint not null,

    -- Party counts live on the allocation, which needs them to size the pitch.
    -- Vehicles are priced but do not constrain occupancy, so they have no home
    -- there and sit here instead.
    vehicles            smallint not null default 0 check (vehicles >= 0),

    -- Which language to write to this guest in, for the rest of the booking's
    -- life. Taken from the site they booked on, not guessed from a later
    -- request's Accept-Language.
    locale              text not null check (locale in ('sv', 'en', 'de')),
    channel             text not null check (channel in ('web', 'desk')),
    notes               text,

    confirmed_at        timestamptz not null default now(),
    created_at          timestamptz not null default now(),
    updated_at          timestamptz not null default now(),

    unique (tenant_id, id),
    unique (tenant_id, reference),
    unique (tenant_id, idempotency_key),
    check (total_net_minor + total_vat_minor = total_gross_minor),
    foreign key (tenant_id) references tenants (id) on delete restrict,
    foreign key (tenant_id, site_id) references sites (tenant_id, id)
        on delete restrict,
    foreign key (tenant_id, category_id)
        references unit_category (tenant_id, id) on delete restrict,
    foreign key (tenant_id, guest_id)
        references guest_identity (tenant_id, id) on delete restrict,
    foreign key (tenant_id, quote_id) references quote (tenant_id, id)
        on delete restrict
);

create index booking_guest_idx on booking (tenant_id, guest_id);
create index booking_confirmed_idx on booking (tenant_id, confirmed_at desc);
alter table booking alter column tenant_id set statistics 1000;

create trigger set_updated_at before update on booking
    for each row execute function set_updated_at();

-- Everyone staying, which is not the same as who booked. A spouse and two
-- children are three more people on the pitch and eventually three more rows on
-- one registerkort, and each carries the same class of personal data as the
-- lead guest — hence its own purge date.
create table booking_party (
    tenant_id     uuid not null default current_tenant_id(),
    id            uuid primary key default uuidv7(),
    booking_id    uuid not null,
    role          text not null check (role in ('lead', 'adult', 'child')),
    given_names   text,
    surname       text,
    -- Required for a child and only for a child: the age band a child prices on
    -- is not recoverable from a count, and guessing it is how a fifteen-year-old
    -- becomes an adult.
    date_of_birth date,
    purge_after   date not null,
    created_at    timestamptz not null default now(),
    unique (tenant_id, id),
    check ((role = 'child') = (date_of_birth is not null)),
    foreign key (tenant_id, booking_id) references booking (tenant_id, id)
        on delete cascade
);

create index booking_party_booking_idx on booking_party (tenant_id, booking_id);
create index booking_party_purge_idx on booking_party (tenant_id, purge_after);

-- The frozen breakdown, copied verbatim from the quote. Append-only: an
-- amendment writes a new amendment_id, and nothing is ever rewritten, because a
-- price line that can be edited is a price nobody can be held to.
--
-- Only the columns the engine actually produces are here. revenue_account and
-- component_bundled belong to invoicing and arrive with the code that writes and
-- reads them; a column nothing fills is a column that reads as zero.
create table booking_price_line (
    tenant_id        uuid not null default current_tenant_id(),
    id               uuid primary key default uuidv7(),
    booking_id       uuid not null,
    amendment_id     uuid not null,
    seq              integer not null,
    kind             text not null,
    stay_date        date,
    description      text not null,
    qty              integer not null,
    unit_gross_minor bigint not null,
    gross_minor      bigint not null,
    net_minor        bigint not null,
    vat_minor        bigint not null,
    vat_code         text not null,
    vat_rate_bp      integer not null,
    vat_treatment    text not null,
    created_at       timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, booking_id, amendment_id, seq),
    check (net_minor + vat_minor = gross_minor),
    foreign key (tenant_id, booking_id) references booking (tenant_id, id)
        on delete cascade
);

create index booking_price_line_booking_idx
    on booking_price_line (tenant_id, booking_id, amendment_id, seq);

create function refuse_mutation() returns trigger
language plpgsql as $$
begin
    raise exception '% is append-only', tg_table_name
        using errcode = 'restrict_violation';
end $$;

create trigger booking_price_line_append_only
    before update or delete on booking_price_line
    for each row execute function refuse_mutation();

-- "Mina sidor" without an account. The emailed link carries a bearer token; the
-- hash is stored and the token itself never is, so a dump of this table cannot
-- be used to read anybody's booking.
create table booking_access_token (
    tenant_id    uuid not null default current_tenant_id(),
    id           uuid primary key default uuidv7(),
    booking_id   uuid not null,
    token_hash   bytea not null,
    expires_at   timestamptz not null,
    last_used_at timestamptz,
    created_at   timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, token_hash),
    foreign key (tenant_id, booking_id) references booking (tenant_id, id)
        on delete cascade
);

create index booking_access_token_booking_idx
    on booking_access_token (tenant_id, booking_id);

-- What happened to this booking, in order. Append-only, because the history of
-- a stay is the answer to every dispute about it, and an editable history
-- answers nothing.
create table reservation_event (
    tenant_id     uuid not null default current_tenant_id(),
    id            uuid primary key default uuidv7(),
    booking_id    uuid not null,
    kind          text not null,
    -- Who: a staff user row, or the guest, or the system itself. A null
    -- actor_user_id with actor 'staff' would be an untraceable change, so the
    -- CHECK ties them together.
    actor         text not null check (actor in ('guest', 'staff', 'system')),
    actor_user_id uuid,
    detail        jsonb,
    created_at    timestamptz not null default now(),
    unique (tenant_id, id),
    check ((actor = 'staff') = (actor_user_id is not null)),
    foreign key (tenant_id, booking_id) references booking (tenant_id, id)
        on delete cascade,
    foreign key (tenant_id, actor_user_id) references users (tenant_id, id)
        on delete restrict
);

create index reservation_event_booking_idx
    on reservation_event (tenant_id, booking_id, created_at);

create trigger reservation_event_append_only
    before update or delete on reservation_event
    for each row execute function refuse_mutation();

-- What this particular stay needs from a physical unit, over and above its
-- category. An accessible pitch is a requirement of a booking, not of a
-- category: the category promises a minimum and this says what this guest
-- cannot do without.
create table allocation_requirement (
    tenant_id  uuid not null default current_tenant_id(),
    id         uuid primary key default uuidv7(),
    booking_id uuid,
    attr_key   text not null check (attr_key in
                   ('accessible', 'electricity_amp', 'pets_allowed',
                    'has_water', 'has_sewer', 'drive_through',
                    'max_vehicle_length_m')),
    op         text not null check (op in ('=', '>=')),
    value      text not null,
    -- A hard filter when true, a scoring preference when false. Only hard
    -- requirements are honoured in v1; the column exists so a preference can be
    -- recorded without being silently enforced.
    required   boolean not null default true,
    created_at timestamptz not null default now(),
    unique (tenant_id, id),
    foreign key (tenant_id, booking_id) references booking (tenant_id, id)
        on delete cascade
);

create index allocation_requirement_booking_idx
    on allocation_requirement (tenant_id, booking_id);

-- Now unit_allocation can point at a booking.
--
-- The original CHECK said a row carries a booking id unless it is a block,
-- which made a hold impossible: a hold is taken while the guest is still
-- filling in their name, before any booking exists. Holds and blocks are both
-- anonymous occupancy; only a confirmed booking names one.
alter table unit_allocation drop constraint unit_allocation_check;

alter table unit_allocation add constraint unit_allocation_booking_ref
    check ((booking_id is null) = (kind in ('block', 'hold')));

-- NOT VALID then VALIDATE: the online path, taken here out of habit rather than
-- need, since only blocks exist yet and none of them carries a booking id.
alter table unit_allocation add constraint unit_allocation_booking_fk
    foreign key (tenant_id, booking_id) references booking (tenant_id, id)
    on delete restrict not valid;
alter table unit_allocation validate constraint unit_allocation_booking_fk;

-- One allocation per booking. v1 sells one category for one date range, so this
-- is what makes "the state of this booking" a single unambiguous row rather
-- than a question about which of several allocations to believe. Selling a
-- booking across two pitches later means dropping an index, which is online.
create unique index unit_allocation_one_per_booking_idx
    on unit_allocation (tenant_id, booking_id) where booking_id is not null;

select rls_enable('booking', 'bokarn_app');
select rls_enable('booking_party', 'bokarn_app');
select rls_enable('booking_price_line', 'bokarn_app');
select rls_enable('booking_access_token', 'bokarn_app');
select rls_enable('reservation_event', 'bokarn_app');
select rls_enable('allocation_requirement', 'bokarn_app');

---- create above / drop below ----

drop index if exists unit_allocation_one_per_booking_idx;
alter table unit_allocation drop constraint if exists unit_allocation_booking_fk;
alter table unit_allocation drop constraint if exists unit_allocation_booking_ref;
alter table unit_allocation add check ((kind = 'block') = (booking_id is null));
drop table if exists allocation_requirement;
drop table if exists reservation_event;
drop table if exists booking_access_token;
drop table if exists booking_price_line;
drop function if exists refuse_mutation() cascade;
drop table if exists booking_party;
drop table if exists booking;
