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
