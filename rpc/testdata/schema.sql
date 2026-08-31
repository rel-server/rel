-- Fixture schema for the rpc package's route-function/JWT-lifecycle tests.
-- Kept separate from pg/testdata/schema.sql (shared by many other
-- packages' tests) so this doesn't bloat that fixture with route-function-
-- specific noise.

create domain "RelHttpRequest" as jsonb;
create domain "RelHttpResponse" as jsonb;
create domain "image/png" as bytea;
create domain "text/plain" as text;

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

-- text-underlying mimetype domain : the return value IS the body directly,
-- no base64 involved (## HTTP's opening paragraphs' generalization beyond
-- bytea-only mimetype domains).
create function fn_text_domain() returns "text/plain" language sql as $$
  select 'hello text domain'::text;
$$;

-- ## Request bodies' (req, files bytea[]) shape : echoes back how many
-- files arrived and their base64 content, so tests can verify a real
-- multipart upload's bytes round-trip correctly.
create function fn_upload(req "RelHttpRequest", files bytea[]) returns "RelHttpResponse" language sql as $$
  select jsonb_build_object(
    'status', 200,
    'content_type', 'application/json',
    'content', jsonb_build_object(
      'body', req->'body',
      'count', coalesce(array_length(files, 1), 0),
      'files', coalesce((select jsonb_agg(encode(f, 'base64')) from unnest(files) f), '[]'::jsonb)
    )
  );
$$;

-- ## Request bodies' (req, files bytea[], parts_headers jsonb) shape :
-- echoes back both the decoded (UTF-8) file contents and the parts_headers
-- metadata rel built, so tests can verify name/filename/content_type/
-- headers per part.
create function fn_upload_with_headers(req "RelHttpRequest", files bytea[], parts_headers jsonb) returns "RelHttpResponse" language sql as $$
  select jsonb_build_object(
    'status', 200,
    'content_type', 'application/json',
    'content', jsonb_build_object(
      'parts_headers', parts_headers,
      'contents', coalesce((select jsonb_agg(convert_from(f, 'utf8')) from unnest(files) f), '[]'::jsonb)
    )
  );
$$;

-- Route the anonymous role genuinely cannot reach : EXECUTE revoked from
-- PUBLIC (Postgres's own CREATE FUNCTION default) and never re-granted to
-- "~anonymous", only to app_user. Proves rpc.Route.AnonymousAuthorized —
-- and the handler's fail-fast 401 — actually distinguish "anonymous can't
-- even reach this route" (this function) from "anonymous can call it but
-- the underlying query fails" (fn_secret above, reachable at the route
-- level but denied at the table-select level).
create function fn_app_only() returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'app only');
$$;
revoke execute on function fn_app_only() from public;
grant execute on function fn_app_only() to app_user;

-- Must NEVER be discovered : ## Request bodies' four shapes are matched by
-- TYPE SEQUENCE — files/parts_headers reordered (jsonb before bytea[]) is
-- simply not a recognized shape, same as any other signature mismatch.
create function fn_wrong_shape(req "RelHttpRequest", parts_headers jsonb, files bytea[]) returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'should not be routable');
$$;

-- A real fn__options route : proves the CORS preflight responder never
-- shadows an actual OPTIONS-suffixed route function (specs/04-http-
-- content.md ## CORS ### Preflight handling).
create function fn_optroute__OPTIONS() returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'real options route');
$$;

-- specs/http-content.md ## CSP ### Per-response override : a raw
-- policy string replaces the process-wide default for this one response.
create function fn_csp_override() returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'csp override', 'csp', 'default-src ''none''');
$$;

-- A route function returning a Jet template response (specs/04-http-
-- content.md ## Templates) : template_data carries through to the
-- template unchanged, alongside Req/Nonce.
create function fn_template(req "RelHttpRequest") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object(
    'status', 200,
    'content_type', 'text/html',
    'template', 'greet.jet',
    'template_data', jsonb_build_object('name', 'World')
  );
$$;

-- A route whose template doesn't exist on disk : proves a load failure is
-- a 500, never a silent fallback to resp.content.
create function fn_template_missing() returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/html', 'template', 'does-not-exist.jet');
$$;

-- specs/http-content.md ### Upload destinations' RelUpload domain and
-- the two-function <name>__prepare/<name> family. fn_dest_upload__prepare
-- reads a "reject" query flag to exercise the earliest-rejection path, and
-- an optional "path"/"overwrite" query value to control placement ; the
-- mandatory fn_dest_upload records what it actually received (path/mkdir/
-- overwrite/part/size) into upload_log for tests to assert on.
create domain "RelUpload" as jsonb;

create table upload_log (id serial primary key, upload jsonb not null);
grant all on upload_log to public;
grant all on upload_log_id_seq to public;

create function fn_dest_upload__prepare(req "RelHttpRequest", part jsonb) returns "RelUpload" language plpgsql as $$
declare
  q jsonb := req->'query';
begin
  if q->>'reject' = 'true' then
    raise exception 'rejected by prepare' using errcode = 'RS400';
  end if;
  return jsonb_build_object(
    'path', q->>'path',
    'mkdir', coalesce((q->>'mkdir')::boolean, false),
    'overwrite', coalesce(q->>'overwrite', 'disallow')
  );
end;
$$;

create function fn_dest_upload(req "RelHttpRequest", upload "RelUpload") returns "RelHttpResponse" language plpgsql as $$
begin
  insert into upload_log (upload) values (upload);
  return jsonb_build_object(
    'status', 200,
    'content_type', 'application/json',
    'content', upload
  );
end;
$$;

-- A route the mandatory upload function itself rejects, AFTER bytes are
-- already fully streamed to disk — proves the temp file is deleted and
-- nothing lands at the final path on a mandatory-function failure.
create function fn_dest_fail__prepare(req "RelHttpRequest", part jsonb) returns "RelUpload" language sql as $$
  select jsonb_build_object('path', req->'query'->>'path', 'overwrite', 'allow');
$$;
create function fn_dest_fail(req "RelHttpRequest", upload "RelUpload") returns "RelHttpResponse" language plpgsql as $$
begin
  raise exception 'mandatory function always rejects' using errcode = 'RS422';
end;
$$;

-- Orphan halves : neither should ever become a route on its own.
create function fn_orphan_prepare__prepare(req "RelHttpRequest", part jsonb) returns "RelUpload" language sql as $$
  select jsonb_build_object();
$$;
create function fn_orphan_mandatory(req "RelHttpRequest", upload "RelUpload") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'should not be routable');
$$;

-- specs/http-content.md ### Upload destinations' "Anonymous-route-
-- authorization" : "must pass for BOTH... fail-closed on either." Two pairs,
-- each with EXECUTE revoked from PUBLIC on exactly one half — proves the
-- combined AnonymousAuthorized is false (401 to an anonymous caller) even
-- when the OTHER half is fully anon-reachable, closing the gap the
-- adversarial review flagged (only testing "both revoked" would miss a bug
-- where just one conjunct is checked).
create function fn_dest_anon_prepare_only__prepare(req "RelHttpRequest", part jsonb) returns "RelUpload" language sql as $$
  select jsonb_build_object('overwrite', 'allow');
$$;
create function fn_dest_anon_prepare_only(req "RelHttpRequest", upload "RelUpload") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'ok');
$$;
revoke execute on function fn_dest_anon_prepare_only("RelHttpRequest", "RelUpload") from public;
grant execute on function fn_dest_anon_prepare_only("RelHttpRequest", "RelUpload") to app_user;
-- fn_dest_anon_prepare_only__prepare keeps its default PUBLIC execute grant
-- (anon-reachable) ; only the mandatory half is restricted.

create function fn_dest_anon_mandatory_only__prepare(req "RelHttpRequest", part jsonb) returns "RelUpload" language sql as $$
  select jsonb_build_object('overwrite', 'allow');
$$;
create function fn_dest_anon_mandatory_only(req "RelHttpRequest", upload "RelUpload") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'ok');
$$;
revoke execute on function fn_dest_anon_mandatory_only__prepare("RelHttpRequest", jsonb) from public;
grant execute on function fn_dest_anon_mandatory_only__prepare("RelHttpRequest", jsonb) to app_user;
-- fn_dest_anon_mandatory_only keeps its default PUBLIC execute grant
-- (anon-reachable) ; only the __prepare half is restricted.

-- specs/rpc.md's ambiguous-route bug regression fixture :
-- three overloads of fn_dupe sharing the same (schema, base, verb="") key
-- once discovered — none of the three should ever be routable, including
-- after the second conflict is found (a naive "delete the map entry on
-- conflict" fix would let the THIRD overload silently become the sole
-- owner).
create function fn_dupe() returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'zero-arg overload');
$$;
create function fn_dupe(req "RelHttpRequest") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'one-arg overload');
$$;
create function fn_dupe(req "RelHttpRequest", files bytea[]) returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'two-arg overload');
$$;
