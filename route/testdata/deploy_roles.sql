-- Deployment-shaped connecting role, applied AFTER schema.sql
-- (WithOrderedInitScripts). Every other test in this package connects as
-- the testcontainers module's default superuser (see schema.sql's own
-- note on "~anonymous"/app_user above), which can SET ROLE to anything
-- regardless of grants — silently masking the exact bug this fixture
-- exists to catch. query_user_deploy is a genuine, non-superuser LOGIN
-- role granted membership in every role a request might switch to,
-- matching the deployment prerequisite specs/TODO.md now documents. That
-- grant alone used to not be enough : introspection itself failed under a
-- non-superuser role before SET ROLE was ever attempted (see
-- pg/info_constraint.go's own comment, fixed alongside this fixture, and
-- pg/nonsuperuser_test.go for the same regression covered directly next
-- to that code).
create role query_user_deploy login password 'test-password' nosuperuser;
grant "~anonymous" to query_user_deploy;
grant app_user to query_user_deploy;

-- A REAL (non-stub) credential check via pgcrypto, matching specs/TODO.md's
-- own resolution of the login question : "checks credentials however the
-- developer wants — bcrypt, an extension, whatever". Every other login
-- fixture in schema.sql (fn_login) always succeeds unconditionally — fine
-- for testing dispatch/mint mechanics, but not an example of the pattern
-- itself.
create extension if not exists pgcrypto;

create table users (
  id serial primary key,
  username text not null unique,
  password_hash text not null
);
insert into users (username, password_hash)
values ('alice', crypt('correct horse battery staple', gen_salt('bf')));

-- Deliberately NO grant on "users" to any request-time role : a login
-- route runs under "~anonymous" (nobody is authenticated yet, by
-- definition), and letting the anonymous role read password_hash values
-- directly would defeat the whole point of hashing them. SECURITY DEFINER
-- — same pattern schema.sql's own fn_check_session already uses — runs
-- the check as this function's OWNER instead, so "~anonymous" never needs
-- (and never gets) its own grant on "users". fn_login_with_credentials
-- itself is still discovered from pg_proc (world-readable metadata, like
-- pg_constraint) with no grant needed for THAT.
create function fn_login_with_credentials(req "RelHttpRequest") returns "RelHttpResponse" language plpgsql security definer as $$
declare
  -- req->'body' : application/json's body is already the decoded JSON
  -- value (## Request's own rename motivation) — no ->>/cast needed, unlike
  -- the old "content" field which was always a raw string.
  creds jsonb := req->'body';
  u_row users%rowtype;
begin
  select * into u_row from users where username = creds->>'username';
  if not found or u_row.password_hash <> crypt(creds->>'password', u_row.password_hash) then
    raise exception 'invalid credentials' using errcode = 'RS401';
  end if;
  return jsonb_build_object(
    'status', 200,
    'content_type', 'application/json',
    'content', jsonb_build_object('ok', true),
    'jwt', jsonb_build_object('role', 'app_user')
  );
end;
$$;
