-- Fixture applied by boot/reload_test.go's end-to-end reload test, via
-- reload.cmd (specs/reload.md) : a single new route function, proving a
-- request against the schema a reload just created succeeds where it
-- would have 404'd before.

create function fn_created_by_migration() returns text language sql as $$
  select 'from migration'::text;
$$;

comment on function fn_created_by_migration() is 'route:: path: "/created"';

-- route.applyAnonymousAuthorization requires an explicit grant, never one
-- credited only via PUBLIC's own CREATE FUNCTION default (specs/route.md
-- ## Anonymous route authorization).
grant execute on function fn_created_by_migration() to "~anonymous";
