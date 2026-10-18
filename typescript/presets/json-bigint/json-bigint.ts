// JSON codec preset that keeps every digit of an int8. Copy this file next to your generated database.ts, fix the
// import below to its path, and import it once, before the first query : `import "./json-bigint"`. It needs
// `lossless-json` (bun add lossless-json). docs/content/typescript/index.md ## Codecs.
//
// The file has two halves that must stay together : the `PgWire` block says what an int8 is, and the codec below
// reads and writes exactly that (a safe integer is a number, a larger one a bigint).
import { isInteger, parse, stringify } from "lossless-json"
import { Querier } from "./database"

declare module "./database" {
  interface PgWire {
    int8: number | bigint
  }
}

// A safe integer is a number, a larger one a bigint, anything with a fraction or exponent a number as JSON.parse would
// read it (a numeric beyond 15 digits is rounded : ask for it as text, see the docs).
function parseNumber(text: string): number | bigint {
  const n = Number(text)
  return isInteger(text) && !Number.isSafeInteger(n) ? BigInt(text) : n
}

Querier.prototype.codec = {
  encode: (value) => stringify(value) as string,
  decode: (body) => parse(body as string, undefined, parseNumber),
}
