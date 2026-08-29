# Error Handling

Use https://github.com/samber/oops everywhere and provide context for all errors, taking care of using its immutable pattern

In development mode, errors in web requests are nicely formatted on a special page, displaying the stack trace and error attributes coming from postgres (HINT, MESSAGE, etc.)

Logs log errors attributes

## Error Codes

To keep compatibility with Postgres so that they can be raised in requests, error codes are 5 characters long. They can be returned from any other part of the application, however.

**All** error codes start with the letter `R` (of Rel.)

When an error code is encountered, it is always added as an HTTP header in the response in `X-Rel-Errorcode`. In most JSON-returning endpoints, the errorcode shall be included as an `errorcode` property.
