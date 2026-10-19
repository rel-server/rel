import "./json-bigint"
import * as db from "./database"
import { run } from "../bench"
import { plainRow } from "../bench-row"

await run("json-bigint", db, (i) => ({ ...plainRow(i), big: BigInt(5_000_000_000 + i) as never }))
