# A `timestamp` is sent in CBOR as an instant in the session time zone

CBOR has one date-and-time form, tag 1 (seconds since the epoch), so a `timestamp` is sent as the instant its wall clock stands for in the connection's time zone, the value `ts::timestamptz` gives and the one `CURRENT_TIMESTAMP` stores. The client then has no ISO text to parse. Writes and filters read an instant back the same way. Both timestamp types keep microseconds, as a double of seconds, which a JavaScript `Date` (milliseconds) could not. A `date` is tag 100 (days since 1970-01-01), for compactness.

## Consequences

The client cannot know which zone the server used : a `timestamp` column holding wall-clock readings of another zone reads as the wrong instant. A `tsrange`, and a `jsonb` holding timestamps, keep their JSON text.
