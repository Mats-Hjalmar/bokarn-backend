-- Guests and staff are separate identity populations on separate Kratos
-- instances with separate stores, so a guest session can never be replayed
-- against a staff route.
create database kratos_guest;
create database kratos_staff;
