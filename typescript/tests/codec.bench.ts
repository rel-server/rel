// The default JSON codec, as the baseline of the combos' benchmarks.
import { run } from "../bench/bench"
import * as db from "./generated/database"
import { plainRow } from "../bench/bench-row"

await run("json", db, plainRow)
