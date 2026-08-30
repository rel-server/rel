-- Fixture schema for the rpc package's route-function/JWT-lifecycle tests.
-- Kept separate from pg/testdata/schema.sql (shared by many other
-- packages' tests) so this doesn't bloat that fixture with route-function-
-- specific noise.

create domain "RelHttpRequest" as jsonb;
create domain "RelHttpResponse" as jsonb;
create domain "image/png" as bytea;

-- Roles the JWT lifecycle switches to via SET LOCAL ROLE. The test
-- connection (config.Test()'s postgres superuser) can SET ROLE to either
-- without membership grants (superuser bypasses that check), but once
-- switched, permission checks apply as the new role normally — SET ROLE by
-- a superuser is NOT itself a superuser bypass for what happens after.
create role "~anonymous";
create role app_user;

create table secret_data (id serial primary key, value text not null);
insert into secret_data (value) values ('top secret');
grant select on secret_data to app_user;
-- no grant to "~anonymous" at all : proves a role switch actually restricts.

-- Anonymous echo routes (0-arg and 1-arg forms) — fn_echo1 re-echoes the
-- WHOLE RelHttpRequest as its own content, letting tests assert on exactly
-- what rel built (method/uri/headers/cookies/jwt) without a bespoke
-- decoding function per field.
create function fn_echo0() returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'anon0');
$$;
create function fn_echo1(req "RelHttpRequest") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'application/json', 'content', req);
$$;

-- Role-gated route : only readable as app_user, proving SET LOCAL ROLE
-- actually took effect (not just that a role NAME was accepted).
create function fn_secret() returns "RelHttpResponse" language plpgsql as $$
declare
  v text;
begin
  select value into v from secret_data limit 1;
  return jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', v);
end;
$$;

-- Login : always succeeds, mints app_user. Deliberately named distinctly
-- (auth.* naming isn't required by the mechanism — any route function can
-- mint, gated only by http.functions.auth) so tests can restrict
-- http.functions.auth to just this one and prove fn_echo0/fn_echo1 setting
-- a "jwt" key would NOT be honored.
create function fn_login(req "RelHttpRequest") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object(
    'status', 200,
    'content_type', 'application/json',
    'content', jsonb_build_object('ok', true),
    'jwt', jsonb_build_object('role', 'app_user')
  );
$$;

create function fn_logout() returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'application/json', 'content', jsonb_build_object('ok', true), 'jwt', null);
$$;

-- check_session, toggleable via session_control for both the "rejects"
-- and "allows" test cases without needing two separate functions.
create table session_control (id boolean primary key default true, reject boolean not null default false);
insert into session_control (reject) values (false);

create function fn_check_session(claims jsonb) returns void language plpgsql security definer as $$
begin
  if (select reject from session_control limit 1) then
    raise exception 'Session revoked' using errcode = 'RS401';
  end if;
end;
$$;

-- RSxxx : a route function's own raised exception maps directly to that
-- HTTP status.
create function fn_forbidden() returns "RelHttpResponse" language plpgsql as $$
begin
  raise exception 'nope' using errcode = 'RS403';
end;
$$;

-- A genuine (non-RSxxx) Postgres error : must become a plain-text 500, not
-- an RSxxx-shaped status.
create function fn_boom() returns "RelHttpResponse" language sql as $$
  select to_jsonb(1/0); -- division by zero — a real, unrelated Postgres error
$$;

-- __GET/__POST verb dispatch pair, sharing one base route name.
create function fn_verbtest__GET(req "RelHttpRequest") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'GET');
$$;
create function fn_verbtest__POST(req "RelHttpRequest") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'POST');
$$;

-- Binary / mimetype-domain route : the bytea return IS the body, the
-- domain's own name IS the Content-Type.
create function fn_image() returns "image/png" language sql as $$
  select '\x89504e470d0a1a0a'::bytea;
$$;

-- Must NEVER be discovered : leading underscore opts out unconditionally.
create function _fn_internal(req "RelHttpRequest") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'should not be routable');
$$;
