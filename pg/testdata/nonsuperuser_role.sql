-- A genuine, non-superuser LOGIN role — no grants beyond what a fresh role
-- gets by default. Exists solely so nonsuperuser_test.go can connect as it
-- instead of the testcontainers module's default superuser, which is the
-- ONLY reason FillConstraintInformations' pg_catalog-visibility bug (see
-- info_constraint.go's own comment) went unnoticed : a superuser sees
-- every pg_catalog system table via information_schema.columns, a plain
-- login role does not.
create role plain_login_role login password 'test-password' nosuperuser;
