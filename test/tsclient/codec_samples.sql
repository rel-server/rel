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
