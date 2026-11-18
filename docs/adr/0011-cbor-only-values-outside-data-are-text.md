# A CBOR-only value outside a write's `data` stands for the text a JSON client would send

Dates and times, dates, UUIDs and byte strings are accepted wherever a JSON client can send their text, so a CBOR client never has to know where its values go. In a filter they are typed literals, read through the same encoders as a write (so a filter accepts and refuses what a write does) and bound with a cast. A well-known param and a route's `req.body` get the text a JSON client would send : the hyphenated UUID, `\x` and hex digits, `YYYY-MM-DD`. Dates and times are bound as text, not in binary, because pgx's binary encoding of a Go time overflows near the end of `timestamptz`'s range.

## Consequences

A well-known query is compiled once, with the cast written at each place a param is used, whatever type the param declares. So a date and time param is written as the session zone's wall clock with its offset, once the connection is known : that one text is the same instant under `::timestamptz` and the zone's wall clock under `::timestamp`, as in a write. A route's `req.body` has no cast to read a zone through, so its dates and times are UTC text.
