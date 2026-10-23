-- Applied after ../pg/testdata/schema.sql.

-- A unique constraint that is only checked at commit : a write that breaks it succeeds statement
-- by statement and fails when the transaction commits.
create table deferred_check (id int primary key, n int not null);
alter table deferred_check add constraint deferred_check_n_unique unique (n) deferrable initially deferred;
