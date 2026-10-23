-- Roles for reqauth's tests, applied after ../pg/testdata/schema.sql.
create role "~anonymous";
create role member_role;
grant "~anonymous", member_role to postgres;

-- What a function sees of the session, from inside the transaction.
create function whoami() returns jsonb language sql as $$
  select jsonb_build_object('user', current_user, 'claims', current_setting('rel.jwt.claims', true)::jsonb);
$$;
grant execute on function whoami() to "~anonymous", member_role;

create table reqauth_t (n int);
grant all on reqauth_t to member_role;
