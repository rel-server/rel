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
  -- Proves rel.jwt.claims is already set by the time check_session runs,
  -- and matches this same call's own claims argument exactly.
  if claims is distinct from current_setting('rel.jwt.claims', true)::jsonb then
    raise exception 'rel.jwt.claims does not match check_session''s own claims argument' using errcode = 'RS500';
  end if;
end;
$$;

-- Reads rel.jwt.claims directly, proving it's visible to an ordinary
-- route function too (not just check_session) — missing_ok=true so a
-- connection where it was never set reads as SQL NULL, not an error.
create function fn_claims_setting() returns "RelHttpResponse" language sql as $$
  select jsonb_build_object(
    'status', 200,
    'content_type', 'application/json',
    'content', current_setting('rel.jwt.claims', true)::jsonb
  );
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
-- reads a "reject" query flag to exercise the earliest-rejection path, an
-- optional "path"/"overwrite" query value to control placement, and an
-- optional "max_size" query value to exercise __prepare's per-request
-- tightening of http.max_upload_size ; the mandatory fn_dest_upload records
-- what it actually received (path/mkdir/overwrite/max_size/part/size) into
-- upload_log for tests to assert on.
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
    'overwrite', coalesce(q->>'overwrite', 'disallow'),
    'max_size', (q->>'max_size')::bigint
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
-- fn_dest_anon_prepare_only__prepare is explicitly anon-granted below
-- (the blanket grant at the end of this file) ; only the mandatory half
-- is restricted.

create function fn_dest_anon_mandatory_only__prepare(req "RelHttpRequest", part jsonb) returns "RelUpload" language sql as $$
  select jsonb_build_object('overwrite', 'allow');
$$;
create function fn_dest_anon_mandatory_only(req "RelHttpRequest", upload "RelUpload") returns "RelHttpResponse" language sql as $$
  select jsonb_build_object('status', 200, 'content_type', 'text/plain', 'content', 'ok');
$$;
revoke execute on function fn_dest_anon_mandatory_only__prepare("RelHttpRequest", jsonb) from public;
grant execute on function fn_dest_anon_mandatory_only__prepare("RelHttpRequest", jsonb) to app_user;
-- fn_dest_anon_mandatory_only is explicitly anon-granted below (the
-- blanket grant at the end of this file) ; only the __prepare half is
-- restricted.

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

-- route.applyAnonymousAuthorization no longer credits a function's
-- reachability to "~anonymous" via a PUBLIC-only grant (specs/route.md
-- ## Anonymous route authorization) — every route these tests exercise
-- anonymously needs an EXPLICIT grant now, same as a real deployment
-- would give it. Grant broadly, then re-revoke from "~anonymous"
-- specifically for the functions deliberately locked to app_user above.
grant execute on all functions in schema public to "~anonymous";
revoke execute on function fn_app_only() from "~anonymous";
revoke execute on function fn_dest_anon_prepare_only("RelHttpRequest", "RelUpload") from "~anonymous";
revoke execute on function fn_dest_anon_mandatory_only__prepare("RelHttpRequest", jsonb) from "~anonymous";

-- specs/new-routes.md fixtures : plain json/jsonb-typed functions, matched
-- structurally rather than by RelHttpRequest domain identity, so these
-- coexist with the domain-based fixtures above without either registry
-- picking up the other's functions.

-- Simple GET route, method inferred (no bytea arg, no stream_upload).
create function fn_new_echo0() returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_new_echo0() is 'route:: path: "/new/echo0"';

-- Method inferred POST via a bytea[] second argument.
create function fn_new_upload(req jsonb, files bytea[]) returns jsonb language sql as $$
  select jsonb_build_object('count', coalesce(array_length(files, 1), 0));
$$;
comment on function fn_new_upload(jsonb, bytea[]) is 'route: {"path": "/new/upload"}';

-- A named text path argument, matched against a "{id}" placeholder.
create function fn_new_byid(req jsonb, id text) returns jsonb language sql as $$
  select jsonb_build_object('id', id);
$$;
comment on function fn_new_byid(jsonb, text) is 'route:: path: "/new/items/{id}"';

-- stream_upload : no bytea/bytea[] argument at all, full-control shape (to
-- set "upload" on its response). First call (req->'upload'->'part' set,
-- no size yet) decides destination ; second call (size set) records it.
create table stream_upload_log (id serial primary key, upload jsonb not null);
grant all on stream_upload_log to public;
grant all on stream_upload_log_id_seq to public;

create function fn_new_stream(req jsonb, out resp jsonb, out content jsonb) returns record language plpgsql as $$
declare
  u jsonb := req->'upload';
begin
  if u->'size' is null then
    -- First call : decide destination from query params, matching the old
    -- __prepare fixture's own reject/path/mkdir/overwrite/max_size knobs.
    if (req->'query'->>'reject') = 'true' then
      raise exception 'rejected on first call' using errcode = 'RS400';
    end if;
    resp := jsonb_build_object('upload', jsonb_build_object(
      'path', req->'query'->>'path',
      'mkdir', coalesce((req->'query'->>'mkdir')::boolean, false),
      'overwrite', coalesce(req->'query'->>'overwrite', 'disallow'),
      'max_size', (req->'query'->>'max_size')::bigint
    ));
    content := null;
  else
    -- Second call : bytes already landed, record what we actually got.
    insert into stream_upload_log (upload) values (u);
    resp := jsonb_build_object('status', 200, 'content_type', 'application/json');
    content := u;
  end if;
end;
$$;
comment on function fn_new_stream(jsonb) is 'route:: path: "/new/stream", stream_upload: true';

-- Full-control (two-OUT-column) ordinary route.
create function fn_new_fullcontrol(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  select jsonb_build_object('status', 200), jsonb_build_object('ok', true);
$$;
comment on function fn_new_fullcontrol(jsonb) is 'route:: path: "/new/full"';

-- Middleware : must be full-control shape.
create function fn_new_middleware(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  select null::jsonb, null::jsonb;
$$;
comment on function fn_new_middleware(jsonb) is 'route:: path: "/new", middleware: true';

-- Colliding pair : same path, both GET (overlapping) -> both excluded.
create function fn_new_collide_a(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;
create function fn_new_collide_b(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_new_collide_a(jsonb) is 'route:: path: "/new/collide"';
comment on function fn_new_collide_b(jsonb) is 'route:: path: "/new/collide"';

-- Disjoint methods at the same path : both must coexist.
create function fn_new_disjoint_get(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;
create function fn_new_disjoint_post(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_new_disjoint_get(jsonb) is 'route:: path: "/new/disjoint", method: "GET"';
comment on function fn_new_disjoint_post(jsonb) is 'route:: path: "/new/disjoint", method: "POST"';

-- Reserved path : must never be registered, regardless of declaration.
create function fn_new_reserved(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_new_reserved(jsonb) is 'route:: path: "/auth/oidc/evil/login"';

-- Template + binary return : invalid combination, function disabled.
create domain "application/pdf" as bytea;
create function fn_new_template_binary(req jsonb) returns "application/pdf" language sql as $$
  select '\x255044462d'::bytea;
$$;
comment on function fn_new_template_binary(jsonb) is 'route:: path: "/new/badtemplate", template: "x.jet"';

-- Malformed declaration : discovery must log and skip, not crash the whole
-- registry build.
create function fn_new_malformed(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_new_malformed(jsonb) is 'route:: path: "unterminated';

-- Extra IN argument not present in the path : function disabled.
create function fn_new_extra_arg(req jsonb, extra text) returns jsonb language sql as $$
  select jsonb_build_object('extra', extra);
$$;
comment on function fn_new_extra_arg(jsonb, text) is 'route:: path: "/new/noplaceholder"';

-- A function with no declaration at all, plain doc comment : must not be
-- discovered as a route.
create function fn_new_undeclared(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_new_undeclared(jsonb) is 'just a normal function, nothing to see here';

-- Config-declared, no comment at all : proves discovery from
-- route.<schema>.<function>.* config alone.
create function fn_new_configonly(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;

-- Config overrides a comment declaration, with a warning.
create function fn_new_override(req jsonb) returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_new_override(jsonb) is 'route:: path: "/new/from-comment"';

-- End-to-end HTTP tests (route/e2e_test.go), exercised through testHandler
-- for real, not just BuildRegistry. Anonymous grants given explicitly below
-- ; fn_new_fullcontrol above stays deliberately ungranted, proving the
-- anonymous-401 path.
grant execute on function fn_new_echo0() to "~anonymous";
grant execute on function fn_new_byid(jsonb, text) to "~anonymous";
grant execute on function fn_new_stream(jsonb) to "~anonymous";
grant execute on function fn_new_upload(jsonb, bytea[]) to "~anonymous";

-- A full-control login-style route : mints a JWT and sets a plain cookie,
-- proving applyResponseSideEffects/writeFullControlResponse actually wire
-- jwt/cookies through for the new two-OUT-column shape.
create function fn_new_login(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  select jsonb_build_object(
    'status', 201,
    'jwt', jsonb_build_object('role', 'app_user'),
    'cookies', jsonb_build_object('greeting', 'hello')
  ), jsonb_build_object('ok', true);
$$;
comment on function fn_new_login(jsonb) is 'route:: path: "/new/login", method: "POST"';
grant execute on function fn_new_login(jsonb) to "~anonymous";

-- Raises an RSxxx exception, proving pgerr.Classify's tiering actually
-- surfaces through the new dispatch path's writeErrorForPgErr, not just in
-- pgerr's own unit tests.
create function fn_new_raises(req jsonb) returns jsonb language plpgsql as $$
begin
  raise exception 'forbidden by application logic' using errcode = 'RS403';
end;
$$;
comment on function fn_new_raises(jsonb) is 'route:: path: "/new/raises"';
grant execute on function fn_new_raises(jsonb) to "~anonymous";

-- Single-return jsonb route with a declared template : the other return
-- type is used as the template's Data, per specs/new-routes.md ## Templates.
create function fn_new_templated(req jsonb) returns jsonb language sql as $$
  select jsonb_build_object('name', 'world');
$$;
comment on function fn_new_templated(jsonb) is 'route:: path: "/new/templated", template: "greet.jet"';
grant execute on function fn_new_templated(jsonb) to "~anonymous";

-- specs/new-routes.md ## Middleware fixtures (route/middleware_test.go).

-- Pass-through middleware : merges {"from_a": "a", "shared": "a"} into
-- request.context, no side-effecting fields, no terminal fields.
create function fn_mw_a(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  select jsonb_build_object('content', jsonb_build_object('from_a', 'a', 'shared', 'a')), null::jsonb;
$$;
comment on function fn_mw_a(jsonb) is 'route:: path: "/mw", middleware: true';
grant execute on function fn_mw_a(jsonb) to "~anonymous";

-- A second pass-through middleware at a longer (more specific) prefix,
-- overlapping fn_mw_a's own "shared" key — this one must win (## Middleware
-- : "a later middleware's keys win over an earlier one's on conflict").
create function fn_mw_b(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  select jsonb_build_object('content', jsonb_build_object('from_b', 'b', 'shared', 'b')), null::jsonb;
$$;
comment on function fn_mw_b(jsonb) is 'route:: path: "/mw/chain", middleware: true';
grant execute on function fn_mw_b(jsonb) to "~anonymous";

-- Route under both prefixes above : echoes request.context back so the
-- test can inspect the merged result.
create function fn_mw_echo_context(req jsonb) returns jsonb language sql as $$
  select coalesce(req->'context', 'null'::jsonb);
$$;
comment on function fn_mw_echo_context(jsonb) is 'route:: path: "/mw/chain/echo"';
grant execute on function fn_mw_echo_context(jsonb) to "~anonymous";

-- Rejecting middleware : an RSxxx exception short-circuits the request
-- exactly like any other route function's own.
create function fn_mw_reject(req jsonb, out resp jsonb, out content jsonb) returns record language plpgsql as $$
begin
  raise exception 'rejected by middleware' using errcode = 'RS403';
end;
$$;
comment on function fn_mw_reject(jsonb) is 'route:: path: "/mw/rejected", middleware: true';
grant execute on function fn_mw_reject(jsonb) to "~anonymous";

create function fn_mw_reject_target() returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_mw_reject_target() is 'route:: path: "/mw/rejected/target"';
grant execute on function fn_mw_reject_target() to "~anonymous";

-- Terminal (non-RSxxx) middleware : sets status itself, short-circuiting
-- without an exception — the route it guards must never run.
create function fn_mw_terminal(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  select jsonb_build_object('status', 402), jsonb_build_object('reason', 'payment required');
$$;
comment on function fn_mw_terminal(jsonb) is 'route:: path: "/mw/terminal", middleware: true';
grant execute on function fn_mw_terminal(jsonb) to "~anonymous";

create table mw_terminal_calls (id serial primary key);
grant all on mw_terminal_calls to public;
grant all on mw_terminal_calls_id_seq to public;
create function fn_mw_terminal_target() returns jsonb language sql as $$
  insert into mw_terminal_calls default values;
  select '{}'::jsonb;
$$;
comment on function fn_mw_terminal_target() is 'route:: path: "/mw/terminal/target"';
grant execute on function fn_mw_terminal_target() to "~anonymous";

-- Ordering : three overlapping middleware at "/order" (shortest),
-- "/order/deep" (mid), and "/order/deep/target" itself (equal-length,
-- still a valid prefix match) — each records its own name with a
-- monotonic sequence number, proving shortest-prefix-first execution
-- order. Deliberately NOT at "/" — that would apply to every other
-- fixture's own route in this shared schema too.
create table mw_order_calls (id serial primary key, name text not null);
grant all on mw_order_calls to public;
grant all on mw_order_calls_id_seq to public;

create function fn_mw_order_root(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  insert into mw_order_calls (name) values ('root');
  select null::jsonb, null::jsonb;
$$;
comment on function fn_mw_order_root(jsonb) is 'route:: path: "/order", middleware: true';
grant execute on function fn_mw_order_root(jsonb) to "~anonymous";

create function fn_mw_order_mid(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  insert into mw_order_calls (name) values ('mid');
  select null::jsonb, null::jsonb;
$$;
comment on function fn_mw_order_mid(jsonb) is 'route:: path: "/order/deep", middleware: true';
grant execute on function fn_mw_order_mid(jsonb) to "~anonymous";

create function fn_mw_order_deep(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  insert into mw_order_calls (name) values ('deep');
  select null::jsonb, null::jsonb;
$$;
comment on function fn_mw_order_deep(jsonb) is 'route:: path: "/order/deep/target", middleware: true';
grant execute on function fn_mw_order_deep(jsonb) to "~anonymous";

create function fn_mw_order_target() returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_mw_order_target() is 'route:: path: "/order/deep/target"';
grant execute on function fn_mw_order_target() to "~anonymous";

-- stream_upload : middleware runs once, ahead of the first call only —
-- counts its own invocations so the test can assert exactly 1, not 2, for
-- a full two-call upload.
create table mw_stream_calls (id serial primary key);
grant all on mw_stream_calls to public;
grant all on mw_stream_calls_id_seq to public;

create function fn_mw_stream_counter(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  insert into mw_stream_calls default values;
  select null::jsonb, null::jsonb;
$$;
comment on function fn_mw_stream_counter(jsonb) is 'route:: path: "/new/stream", middleware: true';
grant execute on function fn_mw_stream_counter(jsonb) to "~anonymous";
