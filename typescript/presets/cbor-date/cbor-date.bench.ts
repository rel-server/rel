import "./cbor-date"
import * as db from "./database"
import { run } from "../bench"
import { plainRow } from "../bench-row"

await run("cbor-date", db, (i) => ({
  ...plainRow(i),
  taken_tz: new Date(1_700_000_000_000 + i * 60_000),
  taken_at: new Date(1_700_000_000_000 + i * 60_000),
  content: new Uint8Array(32).fill(i & 255) as never,
}))
