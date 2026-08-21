-- Outbound messages to guests.
--
-- Templates are per operator and per locale because the confirmation email is
-- the operator's voice, not bokarn's, and a campsite that takes German guests
-- writes to them in German. There is no built-in fallback template: an operator
-- with no template for a locale fails loudly at send time rather than mailing a
-- guest something nobody at the campsite wrote. Tenant provisioning seeds a
-- starting set, exactly as it seeds the starting roles.
create table message_template (
    tenant_id  uuid not null default current_tenant_id(),
    key        text not null,
    locale     text not null check (locale in ('sv', 'en', 'de')),
    channel    text not null check (channel in ('email')),
    subject    text not null check (subject <> ''),
    body       text not null check (body <> ''),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    primary key (tenant_id, key, locale),
    foreign key (tenant_id) references tenants (id) on delete restrict
);

create trigger set_updated_at before update on message_template
    for each row execute function set_updated_at();

-- One row per message actually handed to a transport.
--
-- The unique on outbox_message_id is the whole point: the outbox delivers
-- at-least-once, so the dispatcher can and will run a message twice after a
-- crash between the send and the acknowledgement. Writing this row in the same
-- transaction as the send means the second attempt collides here instead of
-- mailing the guest a second confirmation.
create table message_log (
    tenant_id         uuid not null default current_tenant_id(),
    id                uuid primary key default uuidv7(),
    channel           text not null check (channel in ('email')),
    to_address        text not null check (to_address <> ''),
    template_key      text not null,
    locale            text not null,
    booking_id        uuid,
    outbox_message_id uuid not null,
    subject           text not null,
    sent_at           timestamptz not null default now(),
    unique (tenant_id, id),
    unique (tenant_id, outbox_message_id),
    foreign key (tenant_id, booking_id) references booking (tenant_id, id)
        on delete cascade,
    foreign key (tenant_id, outbox_message_id)
        references outbox_message (tenant_id, id) on delete cascade
);

create index message_log_booking_idx on message_log (tenant_id, booking_id);

select rls_enable('message_template', 'bokarn_app');
select rls_enable('message_log', 'bokarn_app');

---- create above / drop below ----

drop table if exists message_log;
drop table if exists message_template;
