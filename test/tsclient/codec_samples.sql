-- One column of each type whose JS value depends on the codec (typescript/presets), for the preset tests to write and read back.
create table hotel.codec_samples (
  id bigint generated always as identity primary key,
  label text not null,
  taken_tz timestamptz,
  taken_at timestamp,
  taken_on date,
  content bytea,
  big bigint,
  price numeric(12, 3),
  token uuid default gen_random_uuid()
);

grant select, insert, update, delete on hotel.codec_samples to "~anonymous";
grant usage on all sequences in schema hotel to "~anonymous";

-- The rows the codec benchmarks read (typescript/presets/bench.ts).
insert into hotel.codec_samples (label, taken_tz, taken_at, taken_on, content, big, price)
select 'bench', timestamptz '2026-01-01 00:00:00+00' + i * interval '1 minute', timestamp '2026-01-01 00:00:00' + i * interval '1 minute',
  date '2026-01-01' + i, decode(repeat('ab', 32), 'hex'), 5000000000 + i, i * 1.125
from generate_series(1, 2000) i;
