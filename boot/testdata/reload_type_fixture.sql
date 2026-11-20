-- Fixture applied by boot/reload_test.go's temp-table reload test, via reload.cmd : drops the type a write's temp
-- table uses and recreates it with one more label.

drop table mood_t;
drop type mood;
create type mood as enum ('calm', 'glad', 'tense');
create table mood_t (id int primary key, m mood not null);
grant all on mood_t to "~anonymous";
