-- Who the booking is for.
--
-- The guest tables are split by retention rule, not by convenience, because the
-- rules genuinely conflict: a registerkort must be destroyed after three months
-- (PMFS 2015:6) while räkenskapsinformation must be kept for seven years
-- (bokföringslagen), and marketing consent is revocable at any time. A single
-- "guest" row cannot satisfy all three, so the identity that must eventually be
-- erased is separated from the billing party that must survive.
--
-- Only guest_identity and consent land here. guest_billing_party and
-- camping_card have no reader until invoicing and check-in proper, and the
-- retention split that forces them is a property of the data rather than of the
-- schema — so landing them later loses nothing while no guest data exists.
--
-- There is deliberately no personnummer column. Dataskyddslagen 3:10 permits
-- processing a personal identity number only where it is "klart motiverat", and
-- a leisure booking is not.

create table guest_identity (
    tenant_id            uuid not null default current_tenant_id(),
    id                   uuid primary key default uuidv7(),
    given_names          text not null check (given_names <> ''),
    surname              text not null check (surname <> ''),
    email                text not null check (position('@' in email) > 1),
    phone                text not null check (phone <> ''),

    -- Known for a child on the booking party, unknown for the lead guest until
    -- check-in asks. Nullable rather than defaulted: a wrong birth date
    -- reclassifies a guest into another age band and charges them for it.
    date_of_birth        date,

    -- Two different facts about the same person, and neither is ever derived
    -- from the other. country_of_residence is the inkvarteringsstatistik key;
    -- citizenship is what decides whether a registerkort is owed. A Dutch
    -- citizen living in Sweden is a Swedish resident and a foreign national at
    -- the same time.
    citizenship          char(2) references countries (code),
    country_of_residence char(2) not null references countries (code),

    marketing_consent_at timestamptz,

    -- The date after which this row must be erased. NOT NULL on purpose: a
    -- nullable retention date reads as "keep forever", which is the one answer
    -- data protection law does not allow. internal/guest owns the single
    -- constant that computes it.
    purge_after          date not null,

    created_at           timestamptz not null default now(),
    updated_at           timestamptz not null default now(),
    unique (tenant_id, id),
    foreign key (tenant_id) references tenants (id) on delete restrict
);

-- One row per person per operator. Guest data retention is per operator, so a
-- guest who books at two campsites is two rows and erasure at one does not
-- touch the other. Email is the identity key because it is the only thing an
-- anonymous checkout reliably supplies.
create unique index guest_identity_email_idx
    on guest_identity (tenant_id, lower(email));

create index guest_identity_purge_idx on guest_identity (tenant_id, purge_after);

create trigger set_updated_at before update on guest_identity
    for each row execute function set_updated_at();

-- Consent is append-only history, not a boolean on the guest. "Did this person
-- agree to marketing on the day we mailed them?" is the question a regulator
-- asks, and a mutable flag cannot answer it.
create table consent (
    tenant_id  uuid not null default current_tenant_id(),
    id         uuid primary key default uuidv7(),
    guest_id   uuid not null,
    kind       text not null check (kind in ('marketing', 'terms')),
    granted    boolean not null,
    source     text not null check (source in ('web', 'desk', 'email')),
    granted_at timestamptz not null default now(),
    unique (tenant_id, id),
    foreign key (tenant_id, guest_id)
        references guest_identity (tenant_id, id) on delete cascade
);

create index consent_guest_idx on consent (tenant_id, guest_id, kind, granted_at);

select rls_enable('guest_identity', 'bokarn_app');
select rls_enable('consent', 'bokarn_app');

-- The guest form asks where the guest lives, so the list has to cover the
-- markets a Nordic campsite actually receives. Kept in the migration rather
-- than the seed because it is reference data the schema depends on, not
-- example data an operator replaces.
insert into countries (code, name) values
    ('AT', 'Österrike'), ('BE', 'Belgien'), ('CH', 'Schweiz'),
    ('CZ', 'Tjeckien'), ('EE', 'Estland'), ('ES', 'Spanien'),
    ('FR', 'Frankrike'), ('GB', 'Storbritannien'), ('IE', 'Irland'),
    ('IS', 'Island'), ('IT', 'Italien'), ('LT', 'Litauen'),
    ('LV', 'Lettland'), ('PL', 'Polen'), ('US', 'USA')
on conflict (code) do nothing;

---- create above / drop below ----

delete from countries where code in
    ('AT','BE','CH','CZ','EE','ES','FR','GB','IE','IS','IT','LT','LV','PL','US');
drop table if exists consent;
drop table if exists guest_identity;
