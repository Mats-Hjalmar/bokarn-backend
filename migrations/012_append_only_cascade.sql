-- Append-only has to mean "cannot be rewritten", not "cannot ever be erased".
--
-- The first version refused every UPDATE and every DELETE on booking_price_line
-- and reservation_event. That is right for a direct write and wrong for a
-- cascade: deleting a booking cascades to its lines, the trigger refuses, and
-- the booking becomes undeletable. Which means the retention purge cannot erase
-- a guest's data when its seven years are up — a table protected into
-- non-compliance.
--
-- The distinction the trigger needs is whether the parent still exists.
-- Referential-integrity cascades run as AFTER ROW triggers on the parent, so by
-- the time a child's BEFORE DELETE fires the parent row is already gone and
-- visible as gone inside the same transaction. A direct DELETE, by contrast,
-- still has its parent sitting there. Reading the parent is therefore an exact
-- test of "am I being cascaded to?", not an approximation of it.

create or replace function refuse_mutation() returns trigger
language plpgsql as $$
begin
    raise exception '% is append-only', tg_table_name
        using errcode = 'restrict_violation';
end $$;

-- Refuses a rewrite of a line, and refuses erasing one on its own, while
-- letting a line go when the booking it belongs to is going.
create function refuse_orphan_mutation() returns trigger
language plpgsql as $$
declare
    parent_id uuid := (to_jsonb(old) ->> 'booking_id')::uuid;
    parent_exists boolean;
begin
    if tg_op = 'UPDATE' then
        raise exception '% is append-only', tg_table_name
            using errcode = 'restrict_violation';
    end if;

    select exists (select 1 from booking where id = parent_id)
      into parent_exists;
    if parent_exists then
        raise exception
            '% may only be deleted with the booking it belongs to',
            tg_table_name
            using errcode = 'restrict_violation';
    end if;
    return old;
end $$;

drop trigger booking_price_line_append_only on booking_price_line;
create trigger booking_price_line_append_only
    before update or delete on booking_price_line
    for each row execute function refuse_orphan_mutation();

drop trigger reservation_event_append_only on reservation_event;
create trigger reservation_event_append_only
    before update or delete on reservation_event
    for each row execute function refuse_orphan_mutation();

---- create above / drop below ----

drop trigger if exists booking_price_line_append_only on booking_price_line;
create trigger booking_price_line_append_only
    before update or delete on booking_price_line
    for each row execute function refuse_mutation();

drop trigger if exists reservation_event_append_only on reservation_event;
create trigger reservation_event_append_only
    before update or delete on reservation_event
    for each row execute function refuse_mutation();

drop function if exists refuse_orphan_mutation();
