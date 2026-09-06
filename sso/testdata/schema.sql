-- Fixture schema for the sso package's OIDC/SAML callback-invocation tests.
-- Kept separate from route/testdata/schema.sql (a different package's own
-- fixture) even though the shapes overlap somewhat.

create domain "RelHttpResponse" as jsonb;

create schema auth;

create role "~anonymous";
create role app_user;

create table users (username text primary key, role text not null);
insert into users (username, role) values ('alice@example.com', 'app_user');

-- Mirrors docs/content/http/authentication.md ## OpenID Connect and SAML's
-- own example : tries a short list of candidate claim/attribute keys
-- (email-like, then username-like), matches the first one found against
-- users.username, mints app_user on a hit, RS401 otherwise. payload is the
-- new {jwt, identity, state} shape (sso/claims.go's ssoCallbackPayload) —
-- identity.claims holds what used to be the whole argument's own "claims"
-- key, back when this function took the flat pre-rework shape directly.
create function auth.sso_callback(payload jsonb) returns "RelHttpResponse"
language plpgsql
security definer
as $$
declare
  candidate_keys text[] := array['email', 'preferred_username', 'username', 'NameID'];
  key text;
  raw jsonb;
  candidate text;
  matched_role text;
begin
  foreach key in array candidate_keys loop
    raw := payload -> 'identity' -> 'claims' -> key;
    if raw is null then
      continue;
    end if;

    candidate := case jsonb_typeof(raw)
      when 'array' then raw ->> 0
      else raw #>> '{}'
    end;

    if candidate is null then
      continue;
    end if;

    select role into matched_role from users where username = candidate limit 1;
    if matched_role is not null then
      return jsonb_build_object('jwt', jsonb_build_object('role', matched_role, 'protocol', payload->'identity'->>'protocol'));
    end if;
  end loop;

  raise exception 'No account matches any known identity claim' using errcode = 'RS401';
end;
$$;

-- Test-only fixture, never referenced by docs — echoes the callback
-- payload back verbatim as the response body, so a Go test can assert on
-- exactly what sso/claims.go's invokeCallback assembled (jwt/identity/state)
-- without needing a real users-table lookup for every payload-shape test.
create function auth.echo_payload(payload jsonb) returns "RelHttpResponse"
language sql
as $$
  select jsonb_build_object('status', 200, 'content_type', 'application/json', 'content', payload);
$$;
