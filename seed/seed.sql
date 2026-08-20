-- Local development data: two operators, so that every isolation test has a
-- second tenant to fail against. The UUIDs are fixed and inlined into the smoke
-- tests in the plan; do not regenerate them.
--
-- Runs as a superuser, which bypasses RLS, so every insert names its tenant_id
-- explicitly rather than relying on the current_tenant_id() default. Idempotent.

begin;

insert into tenants (id, slug, name, org_no, country, currency, locale, timezone)
values
    ('11111111-1111-1111-1111-111111111111', 'storsand',
     'Storsands Camping AB', '556677-8890', 'SE', 'SEK', 'sv', 'Europe/Stockholm'),
    ('22222222-2222-2222-2222-222222222222', 'hamnviken',
     'Hamnvikens Stugby AB', '551122-3344', 'SE', 'SEK', 'sv', 'Europe/Stockholm')
on conflict (id) do nothing;

insert into sites (tenant_id, id, name, slug, municipality, country, timezone)
values
    ('11111111-1111-1111-1111-111111111111',
     'aaaaaaaa-0000-4000-8000-000000000001',
     'Storsand', 'storsand', 'Halmstad', 'SE', 'Europe/Stockholm'),
    ('22222222-2222-2222-2222-222222222222',
     'bbbbbbbb-0000-4000-8000-000000000001',
     'Hamnviken', 'hamnviken', 'Västervik', 'SE', 'Europe/Stockholm')
on conflict (id) do nothing;

-- Every operator starts with a usable role set. Custom roles mean a freshly
-- created tenant would otherwise have none, and its first staff member could do
-- nothing at all. Operators are free to edit or replace these.
insert into roles (tenant_id, id, name, description)
select t.id, uuidv7(), r.name, r.description
from (values
    ('11111111-1111-1111-1111-111111111111'::uuid),
    ('22222222-2222-2222-2222-222222222222'::uuid)
) as t(id)
cross join (values
    ('Administratör',  'Full behörighet'),
    ('Reception',      'Bokningar, in- och utcheckning, gästregister'),
    ('Prissättning',   'Priser, säsonger, kampanjer och platsutbud'),
    ('Läsbehörighet',  'Endast läsning')
) as r(name, description)
on conflict (tenant_id, name) do nothing;

-- Läsbehörighet is deliberately granted nothing: a route that declares the
-- staff scheme without a permission is readable by any member of the tenant,
-- and permissions gate the operations that change something.
insert into role_permissions (tenant_id, role_id, permission_key)
select r.tenant_id, r.id, p.key
from roles r
join permissions p on true
where r.name = 'Administratör'
on conflict do nothing;

insert into role_permissions (tenant_id, role_id, permission_key)
select r.tenant_id, r.id, p.key
from roles r
join permissions p
  on p.key in ('bookings.manage', 'frontdesk.operate', 'guests.read_registration')
where r.name = 'Reception'
on conflict do nothing;

insert into role_permissions (tenant_id, role_id, permission_key)
select r.tenant_id, r.id, p.key
from roles r
join permissions p on p.key in ('pricing.manage', 'inventory.manage')
where r.name = 'Prissättning'
on conflict do nothing;


-- ---------------------------------------------------------------------------
-- Inventory. Storsand is a mid-sized campsite: 40 electric pitches and 8
-- cabins. Hamnviken is cabins only, so the isolation tests always have a second
-- operator whose inventory looks nothing like the first.
-- ---------------------------------------------------------------------------

insert into unit_category
    (tenant_id, id, site_id, code, name, kind, revenue_class,
     max_occupancy, min_electricity_amp, pets_allowed, accessible, sanitary,
     sort_order)
values
    ('11111111-1111-1111-1111-111111111111',
     'cccccccc-0000-4000-8000-000000000001',
     'aaaaaaaa-0000-4000-8000-000000000001',
     'pitch_el', 'Tomt med el 16A', 'tomt', 'pitch', 6, 16, true, false, false, 1),
    ('11111111-1111-1111-1111-111111111111',
     'cccccccc-0000-4000-8000-000000000002',
     'aaaaaaaa-0000-4000-8000-000000000001',
     'stuga4', 'Stuga 4 bäddar', 'stuga', 'lodging', 4, null, false, false, true, 2),
    ('11111111-1111-1111-1111-111111111111',
     'cccccccc-0000-4000-8000-000000000003',
     'aaaaaaaa-0000-4000-8000-000000000001',
     'stuga6', 'Stuga 6 bäddar', 'stuga', 'lodging', 6, null, true, true, true, 3),
    ('22222222-2222-2222-2222-222222222222',
     'dddddddd-0000-4000-8000-000000000001',
     'bbbbbbbb-0000-4000-8000-000000000001',
     'stuga4', 'Stuga 4 bäddar', 'stuga', 'lodging', 4, null, true, false, true, 1)
on conflict (id) do nothing;

-- 40 pitches, A01..A40. Every third one is accessible and every fifth has a
-- sea view, so the assigner's attribute-surplus penalty has something to score.
insert into unit
    (tenant_id, site_id, category_id, code, electricity_amp, area_m2,
     max_vehicle_length_m, max_occupancy, pets_allowed, accessible,
     has_water, has_greywater, has_sewer, surface, shade, drive_through,
     sanitary, view, sort_order)
select '11111111-1111-1111-1111-111111111111',
       'aaaaaaaa-0000-4000-8000-000000000001',
       'cccccccc-0000-4000-8000-000000000001',
       'A' || lpad(n::text, 2, '0'),
       16, 100, 9.0, 6, true, n % 3 = 0,
       true, n % 2 = 0, n % 10 = 0,
       'grass', case when n % 4 = 0 then 'partial' else 'sun' end,
       n <= 30, false,
       case when n % 5 = 0 then 'sea' end,
       n
  from generate_series(1, 40) as n
on conflict (tenant_id, site_id, code) do nothing;

insert into unit
    (tenant_id, site_id, category_id, code, max_occupancy, pets_allowed,
     accessible, has_water, sanitary, surface, shade, sort_order)
select '11111111-1111-1111-1111-111111111111',
       'aaaaaaaa-0000-4000-8000-000000000001',
       case when n <= 5 then 'cccccccc-0000-4000-8000-000000000002'::uuid
            else 'cccccccc-0000-4000-8000-000000000003'::uuid end,
       'S' || lpad(n::text, 2, '0'),
       case when n <= 5 then 4 else 6 end,
       n > 5, n = 8, true, true, 'gravel', 'partial', 100 + n
  from generate_series(1, 8) as n
on conflict (tenant_id, site_id, code) do nothing;

insert into unit
    (tenant_id, site_id, category_id, code, max_occupancy, pets_allowed,
     accessible, has_water, sanitary, surface, shade, sort_order)
select '22222222-2222-2222-2222-222222222222',
       'bbbbbbbb-0000-4000-8000-000000000001',
       'dddddddd-0000-4000-8000-000000000001',
       'H' || lpad(n::text, 2, '0'),
       4, true, n = 1, true, true, 'gravel', 'shade', n
  from generate_series(1, 12) as n
on conflict (tenant_id, site_id, code) do nothing;

-- Everything is open for the 2027 season. Storsand's S01 and S02 additionally
-- open over Christmas, which is what makes the season filter observable: a
-- December search must return those two and nothing else.
-- Two seasons, because the demo has to work with today's dates as well as
-- next year's: a seed that only covers a future year makes every search on the
-- guest site return nothing, which looks like a broken product rather than a
-- closed campsite.
insert into unit_season (tenant_id, unit_id, period)
select u.tenant_id, u.id, p.period
  from unit u
  cross join (values
    (daterange('2026-04-01', '2026-10-15')),
    (daterange('2027-04-01', '2027-10-15'))
  ) as p(period)
 where not exists (
     select 1 from unit_season s
      where s.unit_id = u.id and s.period = p.period
 );

insert into unit_season (tenant_id, unit_id, period)
select u.tenant_id, u.id, daterange('2027-12-18', '2028-01-07')
  from unit u
 where u.tenant_id = '11111111-1111-1111-1111-111111111111'
   and u.code in ('S01', 'S02')
   and not exists (
     select 1 from unit_season s
      where s.unit_id = u.id and s.period = daterange('2027-12-18', '2028-01-07')
 );

commit;

\echo 'Seeded tenants:'
select slug, name from tenants order by slug;
\echo 'Roles per tenant:'
select t.slug, r.name, count(rp.permission_key) as permissions
  from roles r
  join tenants t on t.id = r.tenant_id
  left join role_permissions rp on rp.tenant_id = r.tenant_id and rp.role_id = r.id
 group by 1, 2 order by 1, 2;
\echo 'Inventory:'
select t.slug, c.code, count(u.id) as units
  from unit_category c
  join tenants t on t.id = c.tenant_id
  left join unit u on u.category_id = c.id
 group by 1, 2 order by 1, 2;
