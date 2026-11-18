# `infinity` dates and times are text in CBOR

Tag 1 and tag 100 cannot hold `infinity` or `-infinity`, and `null` would lose the difference from a missing value, so these are the text strings Postgres prints, in a column that is otherwise tags.
