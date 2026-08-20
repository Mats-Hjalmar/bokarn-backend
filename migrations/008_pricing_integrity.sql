-- Constraints the pricing tables should have had from the start. Found by
-- adversarial testing: without them a repeated seed silently multiplied every
-- adjuster and season, and the consequences were not visible as duplicates —
-- they were visible as a +33% surcharge where the rule said +10%, and as a
-- price edit that reported success and changed nothing.
--
-- Existing duplicates are removed first, keeping the oldest row of each set,
-- because that is the one the compiler was already choosing.

delete from pricing_adjuster a
 where exists (
     select 1 from pricing_adjuster b
      where b.tenant_id = a.tenant_id
        and b.category_id = a.category_id
        and b.name = a.name
        and b.id < a.id
 );

delete from rate_season a
 where exists (
     select 1 from rate_season b
      where b.tenant_id = a.tenant_id
        and b.rate_plan_id = a.rate_plan_id
        and b.name = a.name
        and b.starts_on = a.starts_on
        and b.id < a.id
 );

-- An adjuster is identified by what it is called. Two rules with one name are
-- not a configuration, they are an accident, and they compound multiplicatively.
alter table pricing_adjuster
    add constraint pricing_adjuster_name_unique
    unique (tenant_id, category_id, name);

-- Two seasons at the same priority covering the same dates make the compiled
-- price depend on which row the planner reached first — so editing the "wrong"
-- one reports success and changes nothing. Different priorities still overlap
-- freely: that layering is how a peak fortnight sits on top of high season.
alter table rate_season
    add constraint rate_season_no_ambiguous_overlap
    exclude using gist (
        tenant_id with =,
        rate_plan_id with =,
        priority with =,
        daterange(starts_on, ends_on, '[]') with &&
    );

-- A percentage over 100 is a refund the operator did not intend to offer.
alter table campaign
    add constraint campaign_percent_within_range
    check (kind <> 'percent' or value <= 10000);

-- A season with no base price gives a whole period away. Zero is expressible as
-- a product priced at zero; a rate plan's base is not the place for it.
alter table rate_season
    add constraint rate_season_base_positive check (base_minor > 0);

alter table rate_season
    add constraint rate_season_extras_not_negative
    check (adult_extra_minor >= 0 and child_extra_minor >= 0
           and pet_minor >= 0 and vehicle_minor >= 0
           and included_adults >= 0 and included_children >= 0);

---- create above / drop below ----

alter table rate_season drop constraint if exists rate_season_extras_not_negative;
alter table rate_season drop constraint if exists rate_season_base_positive;
alter table campaign drop constraint if exists campaign_percent_within_range;
alter table rate_season drop constraint if exists rate_season_no_ambiguous_overlap;
alter table pricing_adjuster drop constraint if exists pricing_adjuster_name_unique;
