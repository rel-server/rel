-- Role fixture for /rel's SET ROLE middleware. Applied AFTER
-- ../pg/testdata/schema.sql (WithOrderedInitScripts, not WithInitScripts —
-- entrypoint scripts otherwise run in filename-alphabetical order, which
-- would run this before the tables it grants on exist). Broad privileges
-- throughout : these tests exercise query/write plumbing under
-- role-switching, not authorization itself — see rpc/testdata/schema.sql
-- for role-gated authorization coverage.
create role "~anonymous";
create role authenticated_user;
grant usage on schema alt_schema to "~anonymous", authenticated_user;
grant all privileges on all tables in schema public, alt_schema to "~anonymous", authenticated_user;
grant all privileges on all sequences in schema public, alt_schema to "~anonymous", authenticated_user;

-- Role-gated table : granted to authenticated_user only, NOT to
-- "~anonymous" — proves SET ROLE actually took effect (an anonymous write
-- must be denied, not merely logged).
create table secret_notes (id serial primary key, note text not null);
revoke all on secret_notes from "~anonymous";
grant all privileges on secret_notes to authenticated_user;
grant all privileges on secret_notes_id_seq to authenticated_user;

-- check_session, toggleable via session_control for both the "rejects" and
-- "allows" test cases without needing two separate functions — same
-- pattern rpc/testdata/schema.sql uses.
create table session_control (id boolean primary key default true, reject boolean not null default false);
insert into session_control (reject) values (false);

-- last_check_session_iat records the "iat" claim check_session was actually
-- called with — lets a test distinguish "check_session saw the pre-renewal
-- token" from "check_session saw the already-renewed token," proving
-- applyRole's own Check-then-Renew ordering (specs/TODO.md's resolved
-- "SET LOCAL ROLE / auth timing" entry) actually took effect, not just that
-- renewal happened at all.
create table last_check_session_iat (id boolean primary key default true, iat double precision);
insert into last_check_session_iat (iat) values (null);

create function check_session(claims jsonb) returns void language plpgsql security definer as $$
begin
  update last_check_session_iat set iat = (claims->>'iat')::double precision;
  if (select reject from session_control limit 1) then
    raise exception 'Session revoked' using errcode = 'RS401';
  end if;
end;
$$;
