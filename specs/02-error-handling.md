
Use https://github.com/samber/oops everywhere and provide context for all errors, taking care of using its immutable pattern

In development mode, errors in web requests are nicely formatted on a special page, displaying the stack trace and error attributes coming from postgres (HINT, MESSAGE, etc.)

Logs log errors attributes
