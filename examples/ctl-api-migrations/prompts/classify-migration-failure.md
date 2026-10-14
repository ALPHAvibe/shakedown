Classify the migration failure. Use the db-logs tool if you need the recent database log.

Reply with JSON only:
{"status":"fail","summary":"<cause>","findings":["<one line>"],"data":{"kind":"flake or real"}}

Use kind "flake" when the log looks like a sandbox timeout or a dropped connection. Use kind "real" when the migration itself rejected the schema change.
