// `codec=bigint` over JSON : an int8 keeps every digit, a safe integer is a number and a larger one a bigint, and a
// bigint can be sent. It needs `lossless-json` (bun add lossless-json) : `JSON.parse` rounds a number to a double
// before anything else sees it. docs/content/typescript/index.md ## Codecs.
import { isInteger, parse, stringify } from "lossless-json"
import { Querier } from "../querier"

// A safe integer is a number, a larger one a bigint, anything with a fraction or exponent a number as JSON.parse
// would read it (a numeric beyond 15 digits is rounded : ask for it as text, see the docs).
function parseNumber(text: string): number | bigint {
  const n = Number(text)
  return isInteger(text) && !Number.isSafeInteger(n) ? BigInt(text) : n
}

Querier.prototype.codec = {
  encode: (value) => stringify(value) as string,
  decode: (body) => parse(body as string, undefined, parseNumber),
}
