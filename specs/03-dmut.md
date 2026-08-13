
# DMUT

rel provides `dmut` for handling database migrations/mutations.

## Configuration

* `dmut.directory` (default `/dmut`) : the directory containing the mutations

## Reloading

At any moment, rel can be killed with `SIGUSR1` to have it reload the dmut mutations. When it receives the signal, it waits for the last request to finish before reapplying them.

Whenever dmut mutations are ran, the database is reintrospected afterwards.
