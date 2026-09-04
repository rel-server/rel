-- Fixture schema for tsgen's generator tests — a small "hotel" schema
-- mirroring typescript/schema.example.ts's own worked example : an enum, a
-- composite/domain type, a view, a scalar function, and a set-returning one,
-- plus the two-FK case (hotel.rooms -> both hotel.properties and
-- hotel.room_types) that Relationships must disambiguate by shortcut.

create schema hotel;

create type hotel.room_status as enum ('clean', 'dirty');

create domain hotel.positive_int as integer check (value > 0);

create table hotel.properties (
	id serial primary key,
	name text not null,
	star_rating int,
	created_at timestamptz not null default now()
);
comment on table hotel.properties is 'a hotel property';

create table hotel.room_types (
	id serial primary key,
	name text not null,
	base_price numeric not null
);

create table hotel.rooms (
	id serial primary key,
	property_id int not null references hotel.properties (id),
	room_type_id int not null references hotel.room_types (id),
	room_number text not null,
	floor int,
	status hotel.room_status not null default 'clean',
	nightly_cap hotel.positive_int
);
create index idx_rooms_property on hotel.rooms (property_id);
create index idx_rooms_room_type on hotel.rooms (room_type_id);

create view hotel.clean_rooms as
	select id, property_id, room_type_id, room_number, floor, status, nightly_cap
	from hotel.rooms
	where status = 'clean';

create function hotel.property_average_rating(property hotel.properties) returns numeric
	language sql as $$ select 4.5 $$;

-- overload : same name, different signature (regression for Functions'
-- own key uniqueness in the generated TypeScript).
create function hotel.property_average_rating(property_id int) returns numeric
	language sql as $$ select 4.5 $$;

-- zero-argument function : regression for `args`' own EmptyObject fallback
-- (tsgen), rather than emitting a literal, biome-flagged `{}`.
create function hotel.property_count() returns bigint
	language sql as $$ select count(*) from hotel.properties $$;

create function hotel.rooms_available(property_id int, on_date date default null)
	returns setof hotel.properties
	language sql as $$ select * from hotel.properties where id = property_id $$;

-- deliberately left unindexed on its own FK column, so eligibility filtering
-- (Relations.md ### Join eligibility, mirrored by Relationships' own doc
-- comment) has a real case to exclude.
create table hotel.staff (
	id serial primary key,
	property_id int not null references hotel.properties (id)
);

-- A second schema whose OWN property_average_rating shadows hotel's for the
-- BARE name, once search_path prefers it (below) — regression for
-- bareNameWinners' schema-priority disambiguation (tsgen) : the bare name
-- resolves to alt's version (FunctionsByName), even though
-- hotel.properties' own ComputedProperties entry still lists
-- property_average_rating regardless — the two are deliberately decoupled,
-- see tsgen's renderComputedProperties doc comment.
create schema alt;
create function alt.property_average_rating(x int) returns text
	language sql as $$ select 'n/a' $$;

-- Structurally eligible (takes hotel.properties as its own first argument,
-- arity 1) but in a DIFFERENT schema than hotel.properties itself —
-- regression for ComputedProperties' own same-schema restriction : this
-- must NOT appear as one of hotel.properties' computed properties, even
-- though it otherwise qualifies.
create function alt.property_score(p hotel.properties) returns int
	language sql as $$ select 0 $$;

alter role all set search_path to alt, hotel, public;
