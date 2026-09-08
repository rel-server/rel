-- Fixture schema for pg package introspection tests.
-- Each section exercises one specific bug/behaviour discussed while building
-- this package ; see pg/info_test.go for what each one is checked against.

-- plain all-IN function (regression : proallargtypes/proargmodes are NULL here)
create function fn_plain_add(a int, b int) returns int language sql as $$ select a + b $$;
comment on function fn_plain_add(int, int) is 'adds two numbers';

-- composite FK with non-alphabetical true pairing (regression : conkey/confkey
-- ordering vs information_schema's independently-alphabetized target order)
create table target_t (x int, y int, z int, unique (x, y));
create table src_t (
	id serial primary key,
	b int not null,
	a int not null,
	constraint fk_composite foreign key (b, a) references target_t (y, x)
);
create index idx_src_composite on src_t (b, a);

-- classic to-many : one director has many movies, referencing column indexed
-- (also carries COMMENT ON, regression for comment introspection)
-- studio : director's own outgoing FK target (nullable), added purely so a
-- write-path benchmark (query/write_bench_test.go) has a genuine 3-level
-- outgoing chain to exercise (movie -> director -> studio) — nothing in the
-- existing fixture chained an outgoing relation two levels deep. Nullable so
-- every existing director-inserting test (none of which supply studio_id)
-- is unaffected.
create table studio (id serial primary key, name text not null);
create table director (
	id serial primary key,
	name text not null,
	studio_id int references studio (id)
);
comment on table director is 'a film director';
comment on column director.name is 'the director''s full name';
create index idx_director_studio on director (studio_id);
create table movie (
	id serial primary key,
	director_id int not null references director (id),
	title text not null
);
create index idx_movie_director on movie (director_id);

-- an FK deliberately left unindexed, to exercise the hard-error path
create table unindexed_parent (id serial primary key);
create table unindexed_child (
	id serial primary key,
	parent_id int not null references unindexed_parent (id)
);

-- two distinct FKs from the same table to the same other table
create table customer (id serial primary key);
create table order_t (
	id serial primary key,
	customer_id int not null references customer (id),
	billing_customer_id int not null references customer (id)
);
create index idx_order_customer on order_t (customer_id);
create index idx_order_billing on order_t (billing_customer_id);

-- non-FK join eligibility : unique on the parent side, indexed on the child
-- side, but the child side is NOT unique (so cardinality should still be
-- to-many despite the join being "eligible" via the parent's uniqueness)
create table profile (id serial primary key, user_email text not null unique);
create table account (id serial primary key, email text not null);
create index idx_account_email on account (email);

-- an incoming child whose parent (profile) is written with a non-PK
-- on_conflict target (user_email) : exercises the write path's
-- keysColumns/recoverKeys mechanism, which must recover profile's real id
-- (not just the on_conflict column) for this child to correlate against.
create table profile_note (
	id serial primary key,
	profile_id int not null references profile (id),
	note text not null
);
create index idx_profile_note_profile on profile_note (profile_id);

-- index shapes : covering (INCLUDE), partial, expression, plain composite
create table orders (
	id serial primary key,
	customer_id int,
	total numeric,
	note text,
	flag boolean
);
create index idx_covering on orders (customer_id) include (total);
create index idx_plain on orders (customer_id, total);
create unique index idx_partial_only on orders (flag) where note is not null;
create index idx_expr_only on orders (lower(note));

-- search path / name-index resolution : a second schema with a same-named
-- relation, to exercise search-path ordering
create schema alt_schema;
create table alt_schema.director (id serial primary key, name text not null);

-- function overload set, by arity
create function fn_overload(a int) returns int language sql as $$ select a $$;
create function fn_overload(a int, b int) returns int language sql as $$ select a + b $$;

-- AcceptsArity : defaults shrink the minimum, variadic shrinks it further
-- and removes the maximum
create function fn_with_default(a int, b int default 10) returns int language sql as $$ select a + b $$;
create function fn_variadic(a int, variadic rest int[]) returns int language sql as $$ select a + coalesce(array_length(rest, 1), 0) $$;
-- purely variadic : no required arguments at all, minimum callable arity 0
create function fn_pure_variadic(variadic rest int[]) returns int language sql as $$ select coalesce(array_length(rest, 1), 0) $$;
-- every argument defaulted : minimum callable arity 0
create function fn_all_defaults(a int default 1, b int default 2) returns int language sql as $$ select a + b $$;

-- genuinely ambiguous overload : same arity, only distinguished by argument
-- type, which pass 1's resolver deliberately does not match on (see
-- specs/query-engine.md's Open Question 1) — a positional call is
-- expected to be rejected as ambiguous, not silently guessed
create function fn_ambig(a int) returns int language sql as $$ select a $$;
create function fn_ambig(a text) returns text language sql as $$ select a $$;

-- table-valued function : returns SETOF a known relation, so a
-- function-rooted node should resolve QueryNode.Relation via
-- GetRelationByType and be joinable into, exactly like the table itself
create function fn_directors() returns setof director language sql as $$ select * from director $$;

-- single-row (non-SETOF) composite-return function : returns ONE director
-- row, not a set, but a real, indexed relation type just like fn_directors
-- above — Relation resolves via GetRelationByType exactly the same way,
-- ReturnsSet is just false instead of true. Exercises the distinction
-- between "genuinely scalar, no Relation at all" (fn_plain_add) and "a
-- real composite row, just not a set" : both have ReturnsSet == false, but
-- only the former should skip compileNode's ordinary select/join/where
-- handling — this one should be joinable and select-able exactly like
-- fn_directors, just naturally producing one row instead of many.
create function fn_one_director(p_id int) returns director language sql as $$ select * from director where id = p_id $$;

-- row-type-taking computed column (specs/query-engine.md's own "## Scoping"
-- example : "a function taking the relation's row type as its argument,
-- callable via alias.func_name or func_name(alias)") — exercises a bare
-- self-alias reference reaching compileResolvedField as a *QueryNode
-- (query/sql_expr.go), not a plain column.
create function director_display_name(d director) returns text language sql as $$ select d.name || ' (director)' $$;

-- computed field returning SETOF another relation — a computed field's
-- return type is independent of its eligibility, which only constrains its
-- (first) argument type.
create function director_movies(d director) returns setof movie
language sql as $$ select * from movie where movie.director_id = d.id $$;

-- computed-field / real-column name collision (a schema-authoring problem,
-- not a query-time concern) : eligible by every other rule, but movie
-- already has its own "title" column. The column must win — this function
-- must never be registered as movie's computed field.
create function title(m movie) returns text language sql as $$ select upper(m.title) $$;

-- cross-schema function taking director's own row type : eligible by every
-- other rule, but declared in alt_schema, not director's own schema
-- (public). Must never be registered as a computed field.
create function alt_schema.director_tagline(d director) returns text
language sql as $$ select d.name || ' - cross schema' $$;

-- parameterized table-valued function, embeddable as a JOIN child : exercises
-- pass 2's correlated function-argument resolution (a function node's own
-- "arguments" resolve against its PARENT's scope, since node.Parent != nil
-- once embedded — see ResolveExpressions in expression_resolve.go). Takes
-- the same director_id the plain movie table itself carries, purely so a
-- query can pass the parent's own "id" as a correlated argument here.
create function fn_movies_by_director(p_director_id int) returns setof movie language sql as $$ select * from movie where director_id = p_director_id $$;

-- RETURNS TABLE(...) : an anonymous record, not a real composite type —
-- every RETURNS TABLE/OUT-parameter function shares the one generic
-- pg_catalog.record pseudo-type as its prorettype, so this is exactly the
-- shape pg.Function.RecordRelation exists for (see its own doc comment).
-- director_id is included specifically so a query can use this as the
-- PARENT/outer side of an outgoing join out to the real director table
-- (no index needed on this side, per ## Join eligibility) — the genuinely
-- different, harder direction (this function as the CHILD/joined-into
-- side of some other query) must stay rejected.
create function movie_counts_by_director()
returns table(director_id int, movie_count bigint)
language sql
as $$ select director_id, count(*) from movie group by director_id $$;

-- no primary key at all : on_conflict must be left unresolved (not panic)
-- when unspecified and there's no PK to default to
create table no_pk_t (a int, b int);

-- CHECK constraint fixture : no other table in this schema carries one, so
-- a write's CHECK violation (23514) was previously only ever classified
-- against a synthetic pgconn.PgError, never driven through a real write.
create table checked_t (id serial primary key, amount int not null check (amount > 0));

-- pass 2 fixtures : a composite type used by two distinct columns
-- (regression : the same *pg.Column pointer is reachable via either
-- column's composite navigation, so occurrence-counting/extraction must key
-- on {node, column path}, not the terminal *pg.Column alone), plus a jsonb
-- column for ->/->>/#>/#>> opaque-navigation tests.
create type addr_t as (street text, city text);
create table venue (
	id serial primary key,
	name text not null,
	home addr_t,
	work addr_t,
	metadata jsonb
);

-- venue's child, purely so a composite sub-field write (venue.home/work) can
-- be exercised alongside a nested child in the same request — no fixture
-- table combined the two before.
create table venue_amenity (
	id serial primary key,
	venue_id int not null references venue (id),
	name text not null
);
create index idx_venue_amenity_venue on venue_amenity (venue_id);

-- domain over a composite type, used as a FIELD of another composite type
-- (not a top-level table column) : information_schema.columns' udt_name
-- already reports a domain-typed table COLUMN's base type directly, so
-- top-level columns never actually exercise the gap — but a composite
-- type's own field is introspected straight from pg_attribute.atttypid
-- (see the relkind='c' branch above), which does NOT auto-unwrap domains.
-- That's the real path Underlying()/CompositeRelation() exist for.
create domain addr_domain as addr_t;
create type nested_t as (label text, addr addr_domain);
create table depot (
	id serial primary key,
	location nested_t
);

-- array of composite : ["index", "addresses", 1] then "." into the element
-- should be chainable (Postgres supports this directly), distinct from
-- plain composite navigation.
create table warehouse (
	id serial primary key,
	addresses addr_t[]
);

-- Column-flag regression fixture : IsPrimaryKey/IsParOfUnique/IsGenerated
-- (info_column.go/info_constraint.go).
create table flagged_columns (
	id serial primary key,
	code text not null,
	price numeric not null,
	tax numeric generated always as (price * 0.2) stored,
	unique (code)
);
