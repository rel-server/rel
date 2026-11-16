-- Fixture for pg/anonymous_test.go (anonymous role existence) and route/anonymous_test.go (anonymous route
-- authorization), both run under a non-superuser login : they ask about ANOTHER role's privileges, so they must
-- work whatever the connecting role itself can see.

create role plain_login_role login password 'test-password' nosuperuser;

-- probe_role stands in for "the configured anonymous role" : it exists,
-- but is deliberately granted access to only ONE of the two schema/
-- function pairs below.
create role probe_role login password 'test-password' nosuperuser;

-- locked_schema.fn_locked : EXECUTE is granted to PUBLIC by Postgres's own
-- CREATE FUNCTION default, but the schema itself grants no USAGE to
-- anyone beyond its owner — proving the schema-USAGE conjunct actually
-- gates access despite EXECUTE alone being true (the exact footgun the
-- spec's two-conjunct check exists to catch ; checking EXECUTE alone would
-- wrongly call this reachable).
create schema locked_schema;
create function locked_schema.fn_locked() returns jsonb language sql as $$ select '{}'::jsonb $$;

-- open_schema.fn_open : both USAGE and EXECUTE are explicitly granted to
-- probe_role — the genuinely-reachable case.
create schema open_schema;
grant usage on schema open_schema to probe_role;
create function open_schema.fn_open() returns jsonb language sql as $$ select '{}'::jsonb $$;
grant execute on function open_schema.fn_open() to probe_role;

-- default_only_schema.fn_default_only : USAGE is granted to PUBLIC
-- explicitly (unlike a schema's own creation, which grants PUBLIC nothing
-- by default), and EXECUTE is left exactly where Postgres's own CREATE
-- FUNCTION default puts it — PUBLIC, never explicitly granted to
-- probe_role. probe_role genuinely CAN call this function in practice (it
-- inherits both grants via PUBLIC, the same as any other role would) ; the
-- explicit-grant-only anonymous check must still reject it as an
-- anonymous route, precisely because that reachability was never a
-- deliberate decision about the anonymous role specifically.
create schema default_only_schema;
grant usage on schema default_only_schema to public;
create function default_only_schema.fn_default_only() returns jsonb language sql as $$ select '{}'::jsonb $$;
