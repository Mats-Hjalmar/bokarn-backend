-- Prices for the 2027 season, in the shape a Swedish campsite actually sells:
-- a low season either side of a high season, a peak fortnight in mid-July, and
-- cabins that go Saturday-to-Saturday when demand is highest.
--
-- Runs as a superuser, which bypasses RLS, so every insert names its tenant_id.

begin;

-- Swedish rates. Camping pitches and cabins are accommodation at 12%;
-- electricity sold separately is its own supply at 25%, as are fees and extras.
insert into vat_codes
    (tenant_id, code, country, rate_bp, vat_treatment, valid_from, account_code)
select t.id, v.code, 'SE', v.rate_bp, 'standard', '2020-01-01', v.account
  from (values
    ('11111111-1111-1111-1111-111111111111'::uuid),
    ('22222222-2222-2222-2222-222222222222'::uuid)
  ) as t(id)
  cross join (values
    ('logi',    1200, '3001'),
    ('el',      2500, '3002'),
    ('tillagg', 2500, '3003'),
    ('avgift',  2500, '3004')
  ) as v(code, rate_bp, account)
on conflict do nothing;

-- The standard Swedish cancellation ladder, which guests already recognise.
insert into cancellation_policy (tenant_id, id, name)
values ('11111111-1111-1111-1111-111111111111',
        'eeeeeeee-0000-4000-8000-000000000001', 'Standardvillkor'),
       ('22222222-2222-2222-2222-222222222222',
        'eeeeeeee-0000-4000-8000-000000000002', 'Standardvillkor')
on conflict (id) do nothing;

insert into cancellation_band
    (tenant_id, policy_id, days_before_min, days_before_max,
     charge_pct, fixed_fee_minor)
select p.tenant_id, p.id, b.dmin, b.dmax, b.pct, b.fee
  from cancellation_policy p
  cross join (values
    (40, null, 0,   6000),   -- more than 40 days: expeditionsavgift only
    (12, 39,   25,  0),
    (2,  11,   90,  0),
    (0,  1,    100, 0)
  ) as b(dmin, dmax, pct, fee)
on conflict do nothing;

-- One plan per category. Derived plans and member rates come later; a single
-- standard plan is what the seed needs to make every category sellable.
insert into rate_plan
    (tenant_id, id, category_id, code, name, currency, vat_code,
     min_price_minor, max_price_minor, cancellation_policy_id)
values
  ('11111111-1111-1111-1111-111111111111',
   'f0000000-0000-4000-8000-000000000001',
   'cccccccc-0000-4000-8000-000000000001',
   'pitch_el_std', 'Tomt med el – standard', 'SEK', 'logi',
   19500, 89500, 'eeeeeeee-0000-4000-8000-000000000001'),
  ('11111111-1111-1111-1111-111111111111',
   'f0000000-0000-4000-8000-000000000002',
   'cccccccc-0000-4000-8000-000000000002',
   'stuga4_std', 'Stuga 4 bäddar – standard', 'SEK', 'logi',
   69500, 199500, 'eeeeeeee-0000-4000-8000-000000000001'),
  ('11111111-1111-1111-1111-111111111111',
   'f0000000-0000-4000-8000-000000000003',
   'cccccccc-0000-4000-8000-000000000003',
   'stuga6_std', 'Stuga 6 bäddar – standard', 'SEK', 'logi',
   89500, 249500, 'eeeeeeee-0000-4000-8000-000000000001'),
  ('22222222-2222-2222-2222-222222222222',
   'f0000000-0000-4000-8000-000000000004',
   'dddddddd-0000-4000-8000-000000000001',
   'stuga4_std', 'Stuga 4 bäddar – standard', 'SEK', 'logi',
   69500, 199500, 'eeeeeeee-0000-4000-8000-000000000002')
on conflict (id) do nothing;

-- Seasons. Priority breaks the overlap: the peak fortnight sits on top of high
-- season, which sits on top of the shoulder.
insert into rate_season
    (tenant_id, rate_plan_id, name, starts_on, ends_on, priority,
     base_minor, included_adults, adult_extra_minor, pet_minor,
     min_stay, arrival_mask)
select p.tenant_id, p.id, s.name, s.starts_on::date, s.ends_on::date, s.priority,
       round(p.min_price_minor * s.factor)::bigint,
       2, 15000, 7500, s.min_stay, s.arrival_mask
  from rate_plan p
  cross join (values
    ('Lågsäsong 2026',   '2026-04-01', '2026-06-14', 0, 1.00, 1, 127),
    ('Högsäsong 2026',   '2026-06-15', '2026-08-15', 1, 1.70, 2, 127),
    ('Toppsäsong 2026',  '2026-07-05', '2026-07-25', 2, 2.20, 3, 127),
    ('Eftersäsong 2026', '2026-08-16', '2026-10-15', 0, 1.05, 1, 127),
    ('Lågsäsong',        '2027-04-01', '2027-06-14', 0, 1.00, 1, 127),
    ('Högsäsong',        '2027-06-15', '2027-08-15', 1, 1.70, 2, 127),
    ('Toppsäsong',       '2027-07-05', '2027-07-25', 2, 2.20, 3, 127),
    ('Eftersäsong',      '2027-08-16', '2027-10-15', 0, 1.05, 1, 127)
  ) as s(name, starts_on, ends_on, priority, factor, min_stay, arrival_mask)
on conflict do nothing;

-- Six-berth cabins go Saturday-to-Saturday at the peak, which is how they are
-- actually sold and the only way to keep a week from being cut in half.
update rate_season
   set arrival_mask = 32, min_stay = 7
 where name like 'Toppsäsong%'
   and rate_plan_id = 'f0000000-0000-4000-8000-000000000003';

-- 0-3 free, 4-12 half, 13-15 nearly adult. A child needs a birth date for this,
-- which is why the booking form asks for one.
insert into rate_age_band
    (tenant_id, rate_plan_id, code, age_from, age_to, price_per_night_minor)
select p.tenant_id, p.id, b.code, b.age_from, b.age_to, b.price
  from rate_plan p
  cross join (values
    ('0-3',   0,  3,  0),
    ('4-12',  4,  12, 5000),
    ('13-15', 13, 15, 8000)
  ) as b(code, age_from, age_to, price)
on conflict do nothing;

-- Dynamic pricing: the last of anything is worth more, and a booking made the
-- week before arrival is worth less than an empty pitch.
insert into pricing_adjuster
    (tenant_id, category_id, name, trigger, factor_bp, priority)
values
  ('11111111-1111-1111-1111-111111111111',
   'cccccccc-0000-4000-8000-000000000001',
   'Hög beläggning', '{"occupancy_from_bp": 8500}'::jsonb, 11000, 10),
  ('11111111-1111-1111-1111-111111111111',
   'cccccccc-0000-4000-8000-000000000001',
   'Sista minuten', '{"lead_days_from": 0, "lead_days_to": 7}'::jsonb, 9000, 20)
on conflict do nothing;

insert into campaign
    (tenant_id, code, name, kind, value, stay_from, stay_to)
values ('11111111-1111-1111-1111-111111111111', 'SOMMAR10',
        'Sommarkampanj 10%', 'percent', 1000, '2027-06-01', '2027-08-31')
on conflict do nothing;

insert into product
    (tenant_id, sku, name, vat_code, basis, revenue_group, statistics_bucket)
values
  ('11111111-1111-1111-1111-111111111111', 'el_kwh', 'El per kWh', 'el',
   'metered', 'electricity', 'lodging_regular'),
  ('11111111-1111-1111-1111-111111111111', 'bokningsavgift', 'Bokningsavgift',
   'avgift', 'per_stay', 'fees', 'out_of_scope'),
  ('11111111-1111-1111-1111-111111111111', 'stadning', 'Slutstädning',
   'tillagg', 'per_stay', 'services', 'out_of_scope')
on conflict do nothing;

commit;

\echo 'Rate plans and seasons:'
select t.slug, p.code, count(s.id) as seasons,
       min(s.base_minor) as from_minor, max(s.base_minor) as to_minor
  from rate_plan p
  join tenants t on t.id = p.tenant_id
  left join rate_season s on s.rate_plan_id = p.id
 group by 1, 2 order by 1, 2;

-- A week earns 10% off, a fortnight 15%. Long stays are the difference between
-- a profitable shoulder season and an empty one.
begin;
insert into rate_los_discount (tenant_id, rate_plan_id, min_nights, percent_bp)
select p.tenant_id, p.id, d.nights, d.pct
  from rate_plan p
  cross join (values (7, 1000), (14, 1500)) as d(nights, pct)
on conflict do nothing;
commit;
