-- Length-of-stay discounts. The engine has always applied these; until now
-- there was nowhere to configure one, so the rule existed and never fired —
-- which is worse than not having it, because the pipeline looked complete.
--
-- The discount lands as its own negative line rather than by rewriting nightly
-- prices, so a guest can still see what each night cost and why the total is
-- lower.
create table rate_los_discount (
    tenant_id    uuid not null default current_tenant_id(),
    rate_plan_id uuid not null,
    min_nights   smallint not null check (min_nights > 1),
    percent_bp   integer not null check (percent_bp > 0 and percent_bp < 10000),
    created_at   timestamptz not null default now(),
    primary key (tenant_id, rate_plan_id, min_nights),
    foreign key (tenant_id, rate_plan_id)
        references rate_plan (tenant_id, id) on delete cascade
);

select rls_enable('rate_los_discount', 'bokarn_app');

---- create above / drop below ----

drop table if exists rate_los_discount;
