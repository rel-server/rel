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
create table director (id serial primary key, name text not null);
comment on table director is 'a film director';
comment on column director.name is 'the director''s full name';
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
-- specs/query-compiler.md's Open Question 1) — a positional call is
-- expected to be rejected as ambiguous, not silently guessed
create function fn_ambig(a int) returns int language sql as $$ select a $$;
create function fn_ambig(a text) returns text language sql as $$ select a $$;

-- table-valued function : returns SETOF a known relation, so a
-- function-rooted node should resolve QueryNode.Relation via
-- GetRelationByType and be joinable into, exactly like the table itself
create function fn_directors() returns setof director language sql as $$ select * from director $$;

-- no primary key at all : on_conflict must be left unresolved (not panic)
-- when unspecified and there's no PK to default to
create table no_pk_t (a int, b int);
