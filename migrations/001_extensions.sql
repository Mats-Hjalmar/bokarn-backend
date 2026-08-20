-- Extensions and the one shared trigger function every table's updated_at
-- column hangs off. Kept in its own migration because it is infrastructure
-- that predates the tenancy model: the RLS migration that follows needs
-- pgcrypto for digest() and btree_gist is required before any table can carry
-- the occupancy exclusion constraint. Adding btree_gist later would mean an
-- ACCESS EXCLUSIVE rewrite of a populated unit_allocation.

create extension if not exists "pgcrypto";
create extension if not exists "btree_gist";

create function set_updated_at() returns trigger as $$
begin
    new.updated_at = now();
    return new;
end;
$$ language plpgsql;

---- create above / drop below ----

drop function if exists set_updated_at();
drop extension if exists "btree_gist";
drop extension if exists "pgcrypto";
