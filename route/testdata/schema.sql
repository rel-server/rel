-- Fixture schema for the route package's route-function/JWT-lifecycle
-- tests. Kept separate from pg/testdata/schema.sql (shared by many other
-- packages' tests) so this doesn't bloat that fixture with route-function-
-- specific noise.

-- Roles the JWT lifecycle switches to via SET LOCAL ROLE. The test
-- connection (config.Test()'s postgres superuser) can SET ROLE to either
-- without membership grants (superuser bypasses that check), but once
-- switched, permission checks apply as the new role normally — SET ROLE by
-- a superuser is NOT itself a superuser bypass for what happens after.
create role "~anonymous";
create role app_user;

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

-- ## Content-type sniffing fixtures : echo the relevant sniffed_content_type
-- back so a real HTTP test can inspect it.
create function fn_new_sniff_multipart(req jsonb, files bytea[]) returns jsonb language sql as $$
  select req->'parts'->0->'sniffed_content_type';
$$;
comment on function fn_new_sniff_multipart(jsonb, bytea[]) is 'route:: path: "/new/sniff/multipart"';
grant execute on function fn_new_sniff_multipart(jsonb, bytea[]) to "~anonymous";

create function fn_new_sniff_body(req jsonb, files bytea) returns jsonb language sql as $$
  select req->'sniffed_content_type';
$$;
comment on function fn_new_sniff_body(jsonb, bytea) is 'route:: path: "/new/sniff/body"';
grant execute on function fn_new_sniff_body(jsonb, bytea) to "~anonymous";

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

-- Impacts : a missing EXECUTE grant on a covering middleware is an ordinary
-- Postgres permission error (42501), classified 403 the same as any other
-- route call's — never a silent skip, and never the 500 an earlier spec
-- draft claimed. Deliberately NOT granted to "~anonymous".
create function fn_mw_ungranted(req jsonb, out resp jsonb, out content jsonb) returns record language sql as $$
  select null::jsonb, null::jsonb;
$$;
comment on function fn_mw_ungranted(jsonb) is 'route:: path: "/ungranted", middleware: true';
revoke execute on function fn_mw_ungranted(jsonb) from public;
grant execute on function fn_mw_ungranted(jsonb) to app_user;

create function fn_mw_ungranted_target() returns jsonb language sql as $$ select '{}'::jsonb; $$;
comment on function fn_mw_ungranted_target() is 'route:: path: "/ungranted/target"';
grant execute on function fn_mw_ungranted_target() to "~anonymous", app_user;
