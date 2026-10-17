// Typechecked on its own (tsclient_test.go) : a module augmentation applies to the whole program, so it cannot
// live next to the tests that expect the defaults.
import { relation } from "../generated/database"

declare class Decimal {
  toFixed(digits: number): string
}

declare module "../generated/database" {
  interface PgWire {
    int8: bigint
    numeric: Decimal
  }
}

type Expect<T extends true> = T
type Equal<A, B> = [A] extends [B] ? ([B] extends [A] ? true : false) : false

const roomTypes = relation("hotel.room_types", { select: ["*~"] }).get()
type Row = Awaited<typeof roomTypes>[number]

// int8 columns are bigints, numeric ones Decimals, and the brands still tell one column's type from another
export type _Int8IsABigint = Expect<Row["id"] extends bigint ? true : false>
export type _NumericIsADecimal = Expect<Row["base_price"] extends Decimal ? true : false>
export type _NumericIsNoLongerANumber = Expect<
  Equal<Row["base_price"] extends number ? true : false, false>
>
