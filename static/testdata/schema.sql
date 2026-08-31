-- Fixture schema for the static package's own access-control tests
-- (specs/http-content.md ### Access control). Kept separate from
-- rpc/testdata/schema.sql — this package tests only check_static_access's
-- own calling convention and ordering, not the rest of the HTTP surface.

create role "~anonymous";

-- check_static_access, toggleable via a control table so tests can flip
-- between "allow" and "reject" without needing two separate functions —
-- same convention rpc/testdata/schema.sql's fn_check_session already uses.
create table access_control (id boolean primary key default true, reject boolean not null default false);
insert into access_control (reject) values (false);

create function check_static_access(payload jsonb) returns void language plpgsql security definer as $$
begin
  -- RS403, deliberately distinct from the plain 404 a missing file
  -- produces, so a test can tell "the DB call ran and rejected" apart from
  -- "the existence check short-circuited before the DB was ever
  -- consulted" purely from the response status.
  if (select reject from access_control limit 1) then
    raise exception 'access denied' using errcode = 'RS403';
  end if;
end;
$$;
