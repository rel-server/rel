-- The hotel/booking test fixture (see test/README.md) : a schema deep
-- enough in relationships and Postgres types to exercise rel's query engine
-- against something more demanding than a handful of flat tables. Applied
-- directly via postgres.WithInitScripts, not through any migration tool —
-- query_bench/main_test.go only ever needed a schema to exist, not to
-- exercise a particular tool's own migration mechanics (specs/reload.md).

create schema hotel;
create extension btree_gist;

-- ---- types ----

create type hotel.address as (
  street text,
  city text,
  region text,
  postal_code text,
  country text
);

create type hotel.card_summary as (
  brand text,
  last4 char(4),
  expiry_month smallint,
  expiry_year smallint
);

create type hotel.room_status as enum ('clean', 'dirty', 'maintenance', 'out_of_order');
create type hotel.booking_status as enum ('confirmed', 'cancelled', 'checked_in', 'checked_out');
create type hotel.payment_status as enum ('pending', 'completed', 'refunded', 'failed');

-- ---- tables ----

create table hotel.chains (
  id bigint generated always as identity primary key,
  name text not null unique
);
comment on table hotel.chains is 'A hotel group operating one or more properties under a shared brand.';

create table hotel.loyalty_tiers (
  id bigint generated always as identity primary key,
  name text not null unique,
  min_points integer not null default 0
);
comment on table hotel.loyalty_tiers is 'Loyalty program tiers, looked up by name - deliberately a lookup table rather than an enum, unlike hotel.room_status/booking_status/payment_status.';

create table hotel.properties (
  id bigint generated always as identity primary key,
  chain_id bigint references hotel.chains (id),
  name text not null,
  location point,
  star_rating smallint check (star_rating between 1 and 5),
  description text,
  created_at timestamptz not null default now()
);
-- a stored generated column, kept separate from `description` itself so the
-- FTS index below has something cheap to index directly
alter table hotel.properties add column description_search tsvector
  generated always as (to_tsvector('english', coalesce(description, ''))) stored;
create index properties_description_search_idx on hotel.properties using gin (description_search);
comment on table hotel.properties is 'A single hotel property belonging to a chain.';

-- Test-purpose : a table-valued/SETOF function, exercising query.ts's
-- `arguments`-driven function-relations (not a computed column — this is a
-- node in its own right, joinable/selectable like any other relation).
-- Searches hotel.properties.description_search, the FTS column built above.
create function hotel.search_properties(query text) returns setof hotel.properties
language sql stable as $$
  select * from hotel.properties
  where description_search @@ websearch_to_tsquery('english', query)
$$;

create table hotel.room_types (
  id bigint generated always as identity primary key,
  property_id bigint not null references hotel.properties (id),
  name text not null,
  base_price numeric(10, 2) not null,
  capacity smallint not null default 2,
  unique (property_id, name)
);
comment on table hotel.room_types is 'A category of room offered by a property (e.g. Deluxe, Suite), with its own base price and capacity.';

create table hotel.rooms (
  id bigint generated always as identity primary key,
  property_id bigint not null references hotel.properties (id),
  room_type_id bigint not null references hotel.room_types (id),
  room_number text not null,
  floor smallint,
  status hotel.room_status not null default 'clean',
  features text[] not null default '{}',
  unique (property_id, room_number)
);
comment on table hotel.rooms is 'A single physical room within a property.';

-- Computed column, a one-hop lookup through an outgoing relation.
create function hotel.room_effective_rate(room hotel.rooms) returns numeric
language sql stable strict as $$
  select base_price from hotel.room_types where id = room.room_type_id
$$;
comment on function hotel.room_effective_rate(hotel.rooms) is 'Computed column : this room''s nightly rate, from its room type.';

-- Test-purpose : exercises VARIADIC argument-mode introspection. Paired here
-- with hotel.rooms since it's a natural helper over rooms.features (a plain
-- text[] column) — building a display string from it — not because the
-- function itself references the column.
create function hotel.concat_features(sep text, variadic parts text[]) returns text
language sql immutable as $$
  select array_to_string(parts, sep)
$$;

create table hotel.amenities (
  id bigint generated always as identity primary key,
  name text not null unique
);
comment on table hotel.amenities is 'A catalog of amenities a property can offer (pool, gym, parking, ...).';

create table hotel.property_amenities (
  property_id bigint not null references hotel.properties (id),
  amenity_id bigint not null references hotel.amenities (id),
  primary key (property_id, amenity_id)
);
comment on table hotel.property_amenities is 'Many-to-many: which amenities a property offers. Deliberately parallel to rooms.features (a plain array) - same kind of fact, modeled two different ways.';

create table hotel.guests (
  id bigint generated always as identity primary key,
  email text not null unique,
  first_name text not null,
  last_name text not null,
  phone text,
  date_of_birth date,
  billing_address hotel.address,
  loyalty_tier_id bigint references hotel.loyalty_tiers (id),
  created_at timestamptz not null default now()
);
comment on table hotel.guests is 'A person who can book a room.';
comment on column hotel.guests.billing_address is 'Composite-typed column (hotel.address), not a join to a separate table - a guest only ever has one current billing address.';

-- Computed column : simplest possible case, a pure function of the row's own
-- columns.
create function hotel.guest_full_name(guest hotel.guests) returns text
language sql immutable strict as $$
  select guest.first_name || ' ' || guest.last_name
$$;
comment on function hotel.guest_full_name(hotel.guests) is 'Computed column : the guest''s display name.';

-- Test-purpose : exercises the OUT-argument-mode introspection path, which is
-- the uncommon case (proallargtypes/proargmodes only populate for OUT/INOUT/
-- VARIADIC/TABLE) — complements the plain-IN regression test in pg/. Paired
-- here with hotel.guest_full_name (join vs split of a name), not because it's
-- actually related to hotel.guests' own data.
create function hotel.split_name(full_name text, out first_name text, out last_name text)
language sql immutable strict as $$
  select split_part(full_name, ' ', 1), split_part(full_name, ' ', 2)
$$;

create table hotel.payment_methods (
  id bigint generated always as identity primary key,
  guest_id bigint not null references hotel.guests (id),
  card hotel.card_summary not null,
  is_default boolean not null default false
);
comment on table hotel.payment_methods is 'A guest''s saved payment method. Only ever stores a non-reversible summary (brand/last4/expiry) - never a full card number, even in test fixtures.';

create table hotel.bookings (
  id uuid primary key default gen_random_uuid(),
  guest_id bigint not null references hotel.guests (id),
  room_id bigint not null references hotel.rooms (id),
  stay tstzrange not null,
  status hotel.booking_status not null default 'confirmed',
  created_at timestamptz not null default now(),

  -- no two bookings may claim the same room for overlapping periods ;
  -- needs btree_gist for the "=" operator class on room_id (bigint) to be
  -- usable inside a GiST exclusion constraint at all
  exclude using gist (room_id with =, stay with &&)
);
comment on table hotel.bookings is 'A guest''s reservation of a room for a period. UUID-keyed (unlike most other tables here) since bookings are the kind of entity a real system would expose externally by id.';

-- Computed column : derived purely from the row's own `stay` range.
create function hotel.booking_nights(booking hotel.bookings) returns integer
language sql immutable strict as $$
  select extract(day from upper(booking.stay) - lower(booking.stay))::integer
$$;
comment on function hotel.booking_nights(hotel.bookings) is 'Computed column : length of stay, in nights.';

-- Test-purpose : a default argument value (on_date defaults to current_date).
-- FunctionArgument doesn't currently capture default values at all — this
-- exists to surface that gap, not to fix it here.
--
-- Same parameter/column name collision as hotel.booking_stats ; same fix
-- (qualify with the function's own name).
create function hotel.rooms_available(property_id bigint, on_date date default current_date)
returns setof hotel.rooms
language sql stable as $$
  select r.* from hotel.rooms r
  where r.property_id = rooms_available.property_id
  and not exists (
    select 1 from hotel.bookings b
    where b.room_id = r.id
    and b.stay @> on_date::timestamptz
    and b.status in ('confirmed', 'checked_in')
  )
$$;

create table hotel.booking_guests (
  booking_id uuid not null references hotel.bookings (id),
  guest_id bigint not null references hotel.guests (id),
  primary key (booking_id, guest_id)
);
comment on table hotel.booking_guests is 'Many-to-many: every guest actually staying on a booking, beyond just the primary booker on hotel.bookings.guest_id.';

create table hotel.payments (
  id uuid primary key default gen_random_uuid(),
  booking_id uuid not null references hotel.bookings (id),
  payment_method_id bigint references hotel.payment_methods (id),
  amount numeric(10, 2) not null,
  currency char(3) not null default 'USD',
  status hotel.payment_status not null default 'pending',
  paid_at timestamptz
);
comment on table hotel.payments is 'A charge (or refund) against a booking.';

-- Computed column, deliberately NOT a pure function of the row's own columns —
-- it reads a related table, hence STABLE rather than IMMUTABLE (contrast with
-- hotel.guest_full_name / hotel.booking_nights).
create function hotel.booking_total_paid(booking hotel.bookings) returns numeric
language sql stable strict as $$
  select coalesce(sum(amount), 0)
  from hotel.payments
  where booking_id = booking.id and status = 'completed'
$$;
comment on function hotel.booking_total_paid(hotel.bookings) is 'Computed column : sum of completed payments for this booking.';

-- Test-purpose : RETURNS TABLE(...), a genuinely different case from a named
-- composite type. Postgres implements this via OUT-mode pseudo-arguments with
-- prorettype = record, not a registered pg_type/pg_class composite the way
-- Type.IsComposite()/Type.Relation resolve for an ordinary table's row type —
-- worth finding out how this actually introspects rather than assuming.
--
-- The function's own parameter is named property_id, same as the column it
-- filters on (hotel.rooms.property_id) — qualifying with the function's own
-- name (booking_stats.property_id) disambiguates the two.
create function hotel.booking_stats(property_id bigint)
returns table (total_bookings bigint, total_revenue numeric, avg_nights numeric)
language sql stable as $$
  select
    count(distinct b.id),
    coalesce(sum(p.amount), 0),
    avg(extract(day from upper(b.stay) - lower(b.stay)))
  from hotel.bookings b
  join hotel.rooms r on r.id = b.room_id
  left join hotel.payments p on p.booking_id = b.id and p.status = 'completed'
  where r.property_id = booking_stats.property_id
$$;

create table hotel.reviews (
  id bigint generated always as identity primary key,
  guest_id bigint not null references hotel.guests (id),
  property_id bigint not null references hotel.properties (id),
  booking_id uuid references hotel.bookings (id),
  rating smallint not null check (rating between 1 and 5),
  comment text,
  created_at timestamptz not null default now()
);
-- a second, independent FTS corpus from hotel.properties.description_search
alter table hotel.reviews add column comment_search tsvector
  generated always as (to_tsvector('english', coalesce(comment, ''))) stored;
create index reviews_comment_search_idx on hotel.reviews using gin (comment_search);
comment on table hotel.reviews is 'A guest''s review of a property, optionally tied to the specific booking it followed.';

-- Computed column, aggregate over an incoming relation.
create function hotel.property_average_rating(property hotel.properties) returns numeric
language sql stable strict as $$
  select avg(rating)
  from hotel.reviews
  where property_id = property.id
$$;
comment on function hotel.property_average_rating(hotel.properties) is 'Computed column : average review rating for this property.';

create table hotel.rate_plans (
  id bigint generated always as identity primary key,
  property_id bigint not null references hotel.properties (id),
  name text not null,
  refundable boolean not null default true,
  cancellation_window interval not null default '24 hours',
  unique (property_id, name)
);
comment on table hotel.rate_plans is 'A property''s named rate/cancellation policy. cancellation_window is the one interval-typed column in this schema.';

create table hotel.staff (
  id bigint generated always as identity primary key,
  property_id bigint not null references hotel.properties (id),
  manager_id bigint references hotel.staff (id),
  name text not null,
  role text not null
);
comment on table hotel.staff is 'Property staff, self-referencing via manager_id - kept as the fixture''s instance of the self-join case from query-engine.md''s own worked example.';

-- ---- anonymous access ----

-- Lets rel itself (not query_bench, which connects directly via pg and
-- never role-switches) serve unauthenticated requests against this schema —
-- docs/content/getting-started.md's curl examples, and every example
-- throughout docs/content/example-database/, run with no session at all.
-- See docs/content/http/authentication.md ## Roles : without a real
-- pg.query.anonymous_role role in the database, anonymous access is
-- disabled outright and every unauthenticated request gets 401.
create role "~anonymous";
grant usage on schema hotel to "~anonymous";
grant select, insert, update, delete on all tables in schema hotel to "~anonymous";
grant usage on all sequences in schema hotel to "~anonymous";
grant execute on all functions in schema hotel to "~anonymous";
