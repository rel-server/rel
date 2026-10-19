// The default JSON codec, as the baseline of the presets' benchmarks.
import { run } from "../presets/bench"
import * as db from "./generated/database"
import { plainRow } from "../presets/bench-row"

await run("json", db, plainRow)
