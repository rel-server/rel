-- Role every server under test connects as. One shared role keeps auth out of
-- the comparison (benchmarks/README.md ## Fairness) ; it is applied after
-- test/hotel/schema.sql, on the golden database only.
create role bench login password 'bench';
grant usage on schema hotel to bench;
grant all on all tables in schema hotel to bench;
grant all on all sequences in schema hotel to bench;
grant execute on all functions in schema hotel to bench;
-- rel switches to its anonymous role (created by test/hotel/schema.sql, with
-- the same grants) on every request, as PostgREST does with its own anon role.
grant "~anonymous" to bench;
