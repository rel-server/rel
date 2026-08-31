-- Fixture for pg/anonymous_test.go : covers the two new introspection
-- queries specs/authentication.md adds — "# Roles ## Anonymous role
-- existence" (a pg_roles existence check) and "# HTTP ## Anonymous route
-- authorization" (the has_schema_privilege/has_function_privilege
-- two-conjunct check) — run under a non-superuser connecting role, same
-- concern nonsuperuser_test.go already covers for the rest of
-- introspection : both queries ask about ANOTHER role's privileges, never
-- the connecting role's own, so they must keep working regardless of what
-- the connecting role itself can see.

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
create function locked_schema.fn_locked() returns int language sql as $$ select 1 $$;

-- open_schema.fn_open : both USAGE and EXECUTE are explicitly granted to
-- probe_role — the genuinely-reachable case.
create schema open_schema;
grant usage on schema open_schema to probe_role;
create function open_schema.fn_open() returns int language sql as $$ select 1 $$;
grant execute on function open_schema.fn_open() to probe_role;
