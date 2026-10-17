# Benchmarks TODO

Done: compose stack, golden databases, harness, 17 scenarios, equivalence checks, quick profile.

- Run the full profile and publish the headline numbers in the docs.
- Add a JWT variant of the primary-key lookup, to show the cost of authentication.
- Open-loop load (fixed request rate) for honest tail latencies.
- Faster seeding (batched inserts) so the large size builds quickly.
- Aggregates other than `count`: rel cannot call overloaded aggregates such as `sum` or `max`, so there is no aggregate scenario beyond counting.
