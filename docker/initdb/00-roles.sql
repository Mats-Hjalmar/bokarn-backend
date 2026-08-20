-- The three-role split is the entire multi-tenant security posture, so it is
-- created before any schema exists and lives outside tern: a migration cannot
-- safely create the role it is running as.
--
-- FORCE ROW LEVEL SECURITY does not apply to superusers or to BYPASSRLS roles.
-- A service connected as either has every policy silently inert and no test
-- notices, which is why bokarn_app is explicitly NOSUPERUSER NOBYPASSRLS and
-- owns nothing: policies are enforced against it, not merely declared.

create role bokarn_migrator
    login password 'bokarn_migrator'
    nosuperuser bypassrls createrole;

create role bokarn_app
    login password 'bokarn_app'
    nosuperuser nobypassrls nocreatedb nocreaterole;

create role bokarn_platform
    login password 'bokarn_platform'
    nosuperuser bypassrls nocreatedb nocreaterole;

alter database bokarn owner to bokarn_migrator;
alter schema public owner to bokarn_migrator;

revoke create on schema public from public;
grant usage on schema public to bokarn_app, bokarn_platform;

-- The application role never receives TRUNCATE: RLS policies do not constrain
-- it, so a truncate would cross every tenant boundary at once.
alter default privileges for role bokarn_migrator in schema public
    grant select, insert, update, delete on tables to bokarn_app;
alter default privileges for role bokarn_migrator in schema public
    grant select, insert, update, delete on tables to bokarn_platform;
alter default privileges for role bokarn_migrator in schema public
    grant usage, select on sequences to bokarn_app, bokarn_platform;
alter default privileges for role bokarn_migrator in schema public
    grant execute on functions to bokarn_app, bokarn_platform;
