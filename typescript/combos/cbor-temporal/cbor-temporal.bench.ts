import * as db from "./database"
import { run } from "../../bench/bench"

await run("cbor-temporal", db, (i) => ({
  taken_tz: db.asTimestamptz(
    Temporal.Instant.fromEpochMilliseconds(1_700_000_000_000 + i * 60_000),
  ),
  taken_at: db.asTimestamp(Temporal.Instant.fromEpochMilliseconds(1_700_000_000_000 + i * 60_000)),
  taken_on: db.asPgDate(Temporal.PlainDate.from("2026-01-01").add({ days: i })),
  content: new Uint8Array(32).fill(i & 255) as never,
  big: (5_000_000_000 + i) as never,
  price: (i * 1.125) as never,
}))
