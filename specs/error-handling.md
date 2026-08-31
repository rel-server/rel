# Error Handling

Use https://github.com/samber/oops everywhere and provide context for all errors, taking care of using its immutable pattern

In development mode, errors in web requests are nicely formatted on a special page, displaying the stack trace and error attributes coming from postgres (HINT, MESSAGE, etc.)

Logs log errors attributes

## Error Codes

To keep compatibility with Postgres so that they can be raised in requests (`raise exception ... using errcode = '...'`), error codes are 5 characters long, and all start with the letter `R` (of Rel).

The one convention implemented today is `RSxxx` (`pgerr` package, shared between `/rpc` and `/rel`'s `check_session` rejection) — see `rpc.md ## Postgres Exceptions` for the full rule : `xxx` is read directly as the HTTP response status code, and the raised message becomes the response body. There is no separate `X-Rel-Errorcode` header or `errorcode` JSON property — the code is consumed to pick the status, not echoed back verbatim.

> Why: other `R`-prefixed code families are anticipated (error codes "can be returned from any other part of the application") but none exist yet — `RSxxx` is the only convention actually implemented.
