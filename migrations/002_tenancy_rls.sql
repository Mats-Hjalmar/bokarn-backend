-- The multi-tenant isolation model. Every table added after this migration is
-- either tenant-scoped and carries the four policies below, or is global
-- reference data on the allowlist in e2e/rls_catalog_test.go. There is no
-- third category, and the catalog guard fails the build if one appears.

-- current_tenant_id reads the tenant pinned on the current transaction.
--
-- Three properties are load-bearing and each has a distinct failure mode:
--
--   nullif(...)  once any transaction has set a custom GUC, current_setting
--                with missing_ok returns '' (not NULL) for the rest of that
--                backend's life, and ''::uuid raises 22P02. Without nullif the
--                system passes every test on a fresh connection and then throws
--                cast errors under pool reuse.
--   language sql a plpgsql wrapper is not inlined by the planner, so the
--                volatility and parallel markings below stop applying.
--   stable       CREATE FUNCTION defaults to VOLATILE PARALLEL UNSAFE, which
--   parallel     drops the predicate out of index conditions and disables
--   safe         parallel plans on every tenant table.
create function current_tenant_id() returns uuid
language sql stable parallel safe
as $$ select nullif(current_setting('app.tenant_id', true), '')::uuid $$;

-- rls_enable turns on row-level security for one table and installs the four
-- canonical policies.
--
-- FORCE is not optional: without it the table owner is exempt, and the owner is
-- the role migrations run as. Four separate policies rather than one FOR ALL,
-- because FOR ALL silently defaults WITH CHECK to the USING expression, hiding
-- the read/write asymmetry, and a later hand-edit to USING then changes write
-- semantics invisibly.
--
-- tenant_col exists for exactly one table: tenants keys on its own id. Every
-- other caller takes the default.
create function rls_enable(
    tbl regclass,
    app_role name,
    tenant_col name default 'tenant_id'
) returns void
language plpgsql as $$
declare
    pred text := format('%I = current_tenant_id()', tenant_col);
begin
    execute format('alter table %s enable row level security', tbl);
    execute format('alter table %s force row level security', tbl);

    execute format('drop policy if exists tenant_select on %s', tbl);
    execute format('drop policy if exists tenant_insert on %s', tbl);
    execute format('drop policy if exists tenant_update on %s', tbl);
    execute format('drop policy if exists tenant_delete on %s', tbl);

    execute format(
        'create policy tenant_select on %s for select to %I using (%s)',
        tbl, app_role, pred);
    execute format(
        'create policy tenant_insert on %s for insert to %I with check (%s)',
        tbl, app_role, pred);
    execute format(
        'create policy tenant_update on %s for update to %I using (%s) with check (%s)',
        tbl, app_role, pred, pred);
    execute format(
        'create policy tenant_delete on %s for delete to %I using (%s)',
        tbl, app_role, pred);
end;
$$;

-- rls_reapply_all replays rls_enable over every table that already has row
-- level security on. It ships alongside rls_enable so that a future migration
-- changing the policy shape has a one-line way to bring existing tables with
-- it; replacing the function alone would leave the old policies in place.
--
-- The tenant column is derived rather than stored: a table with a tenant_id
-- column keys on it, and the only table without one keys on its own id.
create function rls_reapply_all(app_role name) returns void
language plpgsql as $$
declare
    r record;
    col name;
begin
    for r in
        select c.oid::regclass as tbl, c.relname
        from pg_class c
        join pg_namespace n on n.oid = c.relnamespace
        where n.nspname = 'public' and c.relkind = 'r' and c.relrowsecurity
    loop
        select case when exists (
            select 1 from pg_attribute
            where attrelid = r.tbl and attname = 'tenant_id' and attnum > 0
        ) then 'tenant_id' else 'id' end into col;

        perform rls_enable(r.tbl, app_role, col);
    end loop;
end;
$$;

-- ---------------------------------------------------------------------------
-- Global reference data.
--
-- No tenant_id, no RLS, readable by everyone and writable by no one but the
-- migrator. Each of these is on the allowlist in e2e/rls_catalog_test.go, so
-- adding a fourth requires a code review rather than just a migration.
-- ---------------------------------------------------------------------------

create table currencies (
    code     char(3) primary key,
    exponent smallint not null
);

create table countries (
    code char(2) primary key,
    name text not null
);

-- permissions is the fixed vocabulary of capabilities the code actually
-- enforces. Roles are per-tenant and operators compose their own, but they may
-- only bundle keys from this table: a permission nothing checks would be a
-- promise the system does not keep.
create table permissions (
    key         text primary key,
    description text not null
);

insert into currencies (code, exponent) values
    ('SEK', 2), ('NOK', 2), ('DKK', 2), ('EUR', 2);

insert into countries (code, name) values
    ('SE', 'Sverige'), ('NO', 'Norge'), ('DK', 'Danmark'),
    ('FI', 'Finland'), ('DE', 'Tyskland'), ('NL', 'Nederländerna');

insert into permissions (key, description) values
    ('settings.manage',         'Hantera anläggningar, roller och personal'),
    ('inventory.manage',        'Hantera platser, stugor och kategorier'),
    ('pricing.manage',          'Hantera prisplaner, säsonger och kampanjer'),
    ('bookings.manage',         'Skapa, ändra och avboka bokningar'),
    ('frontdesk.operate',       'Checka in och ut, mätarställningar, gästregister'),
    ('billing.manage',          'Fakturera, kreditera och hantera betalningar'),
    ('loyalty.manage',          'Hantera lojalitetsprogram och poäng'),
    ('guests.read_registration','Läsa registerkort och gästuppgifter'),
    ('audit.read',              'Läsa ändringsloggen');

-- Cross-tenant reads by bokarn's own operators are audited here rather than in
-- audit_log, which is tenant-scoped: a read that spans operators cannot honestly
-- be attributed to one of them. Global, and writable only by the platform role.
create table platform_audit_log (
    id                uuid primary key default uuidv7(),
    actor_external_id text not null,
    action            text not null,
    detail            jsonb,
    at                timestamptz not null default now()
);

create index platform_audit_log_at_idx on platform_audit_log (at desc);

revoke insert, update, delete on currencies, countries, permissions
    from bokarn_app, bokarn_platform;

revoke select, insert, update, delete on platform_audit_log from bokarn_app;

-- ---------------------------------------------------------------------------
-- Tenant-scoped tables.
--
-- Conventions applied without exception:
--   tenant_id defaults to current_tenant_id(), so a store never names it on
--     insert and cannot be tricked into writing another operator's id. Unset
--     tenant yields NULL and the NOT NULL fails the write closed.
--   unique (tenant_id, id) on every parent and composite foreign keys
--     throughout, because foreign key checks bypass RLS entirely: a
--     single-column reference is a cross-tenant existence oracle.
--   natural keys are tenant-scoped, or a duplicate-key error leaks the
--     existence of another operator's row.
--   every index leads with tenant_id.
-- ---------------------------------------------------------------------------

create table tenants (
    id         uuid primary key default uuidv7(),
    slug       text not null unique,
    name       text not null,
    org_no     text,
    country    char(2) not null references countries (code),
    currency   char(3) not null references currencies (code),
    locale     text not null default 'sv',
    timezone   text not null default 'Europe/Stockholm',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now()
);

create table sites (
    tenant_id     uuid not null default current_tenant_id(),
    id            uuid primary key default uuidv7(),
    name          text not null,
    slug          text not null,
    municipality  text,
    country       char(2) not null references countries (code),
    timezone      text not null default 'Europe/Stockholm',
    check_in_time time not null default '15:00',
    check_out_time time not null default '11:00',
    created_at    timestamptz not null default now(),
    updated_at    timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, slug),
    foreign key (tenant_id) references tenants (id) on delete restrict
);

-- One person, one operator: unique (external_user_id) is global on purpose.
-- Staff tenancy comes from the Kratos identity's metadata_public.tenant_id, so
-- an identity that appeared under two tenants would make the pinned tenant
-- depend on which row was found first. The constraint makes that unrepresentable.
create table users (
    tenant_id        uuid not null default current_tenant_id(),
    id               uuid primary key default uuidv7(),
    external_user_id text not null unique,
    email            text,
    name             text,
    last_seen_at     timestamptz,
    created_at       timestamptz not null default now(),
    updated_at       timestamptz not null default now(),
    unique (tenant_id, id),
    foreign key (tenant_id) references tenants (id) on delete restrict
);

create table roles (
    tenant_id   uuid not null default current_tenant_id(),
    id          uuid primary key default uuidv7(),
    name        text not null,
    description text not null default '',
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, name),
    foreign key (tenant_id) references tenants (id) on delete restrict
);

create table role_permissions (
    tenant_id      uuid not null default current_tenant_id(),
    role_id        uuid not null,
    permission_key text not null references permissions (key),
    created_at     timestamptz not null default now(),
    primary key (tenant_id, role_id, permission_key),
    foreign key (tenant_id, role_id) references roles (tenant_id, id)
        on delete cascade
);

create table user_roles (
    tenant_id  uuid not null default current_tenant_id(),
    user_id    uuid not null,
    role_id    uuid not null,
    created_at timestamptz not null default now(),
    primary key (tenant_id, user_id, role_id),
    foreign key (tenant_id, user_id) references users (tenant_id, id)
        on delete cascade,
    foreign key (tenant_id, role_id) references roles (tenant_id, id)
        on delete cascade
);

create index user_roles_role_idx on user_roles (tenant_id, role_id);

-- The actor is written by the handler inside the mutation's own transaction,
-- never by a trigger: only the request layer knows who is acting and why.
create table audit_log (
    tenant_id   uuid not null default current_tenant_id(),
    id          uuid primary key default uuidv7(),
    actor_id    uuid,
    action      text not null,
    entity_type text not null,
    entity_id   text,
    changes     jsonb,
    reason      text,
    at          timestamptz not null default now(),
    unique (tenant_id, id),
    foreign key (tenant_id) references tenants (id) on delete restrict
);

create index audit_log_at_idx on audit_log (tenant_id, at desc);
create index audit_log_entity_idx on audit_log (tenant_id, entity_type, entity_id);
create index audit_log_changes_idx on audit_log using gin (changes);

-- Anything with an external side effect is written here in the same
-- transaction as the state change that justifies it, so a crash between the
-- two is impossible. The dispatcher runs per tenant, hence the tenant-leading
-- index: a global (available_at) index would force a cross-tenant scan the
-- application role cannot perform anyway.
create table outbox_message (
    tenant_id       uuid not null default current_tenant_id(),
    id              uuid primary key default uuidv7(),
    kind            text not null,
    payload         jsonb not null,
    idempotency_key text not null,
    available_at    timestamptz not null default now(),
    attempts        integer not null default 0,
    last_error      text,
    delivered_at    timestamptz,
    created_at      timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, kind, idempotency_key),
    foreign key (tenant_id) references tenants (id) on delete restrict
);

create index outbox_pending_idx on outbox_message (tenant_id, available_at)
    where delivered_at is null;

create trigger set_updated_at before update on tenants
    for each row execute function set_updated_at();
create trigger set_updated_at before update on sites
    for each row execute function set_updated_at();
create trigger set_updated_at before update on users
    for each row execute function set_updated_at();
create trigger set_updated_at before update on roles
    for each row execute function set_updated_at();

-- A guest arrives on storsand.bokarn.se with no session and no operator
-- pinned, so resolving that hostname is the one read that must happen before
-- the tenant is known — and the policy on tenants would return nothing.
--
-- A SECURITY DEFINER function is the narrow way out: it exposes exactly one
-- mapping, slug to id, which is public by construction because the slug IS the
-- hostname. The alternatives were worse: a second lookup table duplicating the
-- source of truth, a bespoke permissive policy that would break the catalog
-- guard's "every predicate is one of two known families" check, or putting a
-- BYPASSRLS connection on every guest request.
--
-- search_path is pinned because a SECURITY DEFINER function that resolves names
-- through the caller's search_path can be hijacked by a same-named relation.
create function tenant_id_for_slug(p_slug text) returns uuid
language sql stable security definer
set search_path = pg_catalog, public
as $$ select id from tenants where slug = p_slug $$;

revoke execute on function tenant_id_for_slug(text) from public;
grant execute on function tenant_id_for_slug(text) to bokarn_app;

select rls_enable('tenants', 'bokarn_app', 'id');
select rls_enable('sites', 'bokarn_app');
select rls_enable('users', 'bokarn_app');
select rls_enable('roles', 'bokarn_app');
select rls_enable('role_permissions', 'bokarn_app');
select rls_enable('user_roles', 'bokarn_app');
select rls_enable('audit_log', 'bokarn_app');
select rls_enable('outbox_message', 'bokarn_app');

---- create above / drop below ----

drop table if exists outbox_message;
drop table if exists audit_log;
drop table if exists user_roles;
drop table if exists role_permissions;
drop table if exists roles;
drop table if exists users;
drop table if exists sites;
drop table if exists tenants;
drop table if exists platform_audit_log;
drop table if exists permissions;
drop table if exists countries;
drop table if exists currencies;
drop function if exists tenant_id_for_slug(text);
drop function if exists rls_reapply_all(name);
drop function if exists rls_enable(regclass, name, name);
drop function if exists current_tenant_id();
