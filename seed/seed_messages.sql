-- Confirmation email wording, per operator and per locale.
--
-- There is no built-in fallback template, on purpose: an operator with no
-- template for a language fails loudly at send time rather than writing to a
-- guest in a language nobody at the campsite chose. That makes seeding a
-- starting set part of provisioning an operator, exactly as seeding a starting
-- role set is — so this file is what tenant creation will do for a real
-- customer, done here for the two development operators.
--
-- Every placeholder the templates use has to be supplied by the code that
-- renders them; a missing one fails the delivery and names the field, rather
-- than mailing a guest a confirmation with a blank booking number.

begin;

insert into message_template (tenant_id, key, locale, channel, subject, body)
select t.id, 'booking_confirmed', v.locale, 'email', v.subject, v.body
  from tenants t
  cross join (values
    ('sv',
     'Din bokning {{.Reference}} är bekräftad',
     E'Hej {{.GuestName}},\n\n'
     'Tack för din bokning hos {{.SiteName}}. Den är bekräftad.\n\n'
     'Bokningsnummer: {{.Reference}}\n'
     'Boende:         {{.Category}}\n'
     'Ankomst:        {{.Arrival}}\n'
     'Avresa:         {{.Departure}}\n'
     'Nätter:         {{.Nights}}\n'
     'Totalt:         {{.Total}}\n\n'
     'Betalning sker på plats vid ankomst. Du behöver inte betala något nu.\n\n'
     'Se din bokning här:\n{{.Link}}\n\n'
     'Välkommen!\n{{.SiteName}}\n'),
    ('en',
     'Your booking {{.Reference}} is confirmed',
     E'Hello {{.GuestName}},\n\n'
     'Thank you for booking with {{.SiteName}}. Your stay is confirmed.\n\n'
     'Booking reference: {{.Reference}}\n'
     'Accommodation:     {{.Category}}\n'
     'Arrival:           {{.Arrival}}\n'
     'Departure:         {{.Departure}}\n'
     'Nights:            {{.Nights}}\n'
     'Total:             {{.Total}}\n\n'
     'Payment is taken on arrival. There is nothing to pay now.\n\n'
     'View your booking:\n{{.Link}}\n\n'
     'We look forward to seeing you.\n{{.SiteName}}\n'),
    ('de',
     'Ihre Buchung {{.Reference}} ist bestätigt',
     E'Hallo {{.GuestName}},\n\n'
     'vielen Dank für Ihre Buchung bei {{.SiteName}}. Sie ist bestätigt.\n\n'
     'Buchungsnummer: {{.Reference}}\n'
     'Unterkunft:     {{.Category}}\n'
     'Anreise:        {{.Arrival}}\n'
     'Abreise:        {{.Departure}}\n'
     'Nächte:         {{.Nights}}\n'
     'Gesamt:         {{.Total}}\n\n'
     'Die Bezahlung erfolgt bei der Anreise vor Ort.\n\n'
     'Ihre Buchung ansehen:\n{{.Link}}\n\n'
     'Wir freuen uns auf Sie.\n{{.SiteName}}\n')
  ) as v(locale, subject, body)
on conflict (tenant_id, key, locale) do update
   set subject = excluded.subject, body = excluded.body;

commit;

\echo 'Message templates:'
select t.slug, m.key, string_agg(m.locale, ', ' order by m.locale) as locales
  from message_template m join tenants t on t.id = m.tenant_id
 group by 1, 2 order by 1, 2;
