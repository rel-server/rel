import * as db from "./database"
import { run } from "../../bench/bench"
import { plainRow } from "../../bench/bench-row"

await run("json-bigint", db, (i) => ({ ...plainRow(i), big: BigInt(5_000_000_000 + i) as never }))
