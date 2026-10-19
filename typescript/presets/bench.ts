// A small benchmark of a codec against a real server, shared by the `*.bench.ts` of each preset (and tests/ for the
// default JSON). Run through `TSCLIENT_BENCH=1 go test -v -run TestTypeScriptClient ./test/tsclient`. Each codec is a
// process of its own, as for the tests : the codec is assigned on the Querier prototype.
//
// It reports, per codec : the bytes of a BENCH_ROWS-row response and of a write of a quarter of that, what `codec.decode` / `codec.encode`
// alone cost on those same bytes (the client's CPU), and the whole request (server and local network included).
type Codec = {
  binary?: boolean
  encode(value: unknown): string | Uint8Array
  decode(body: string | Uint8Array): unknown
}
type Rel = {
  get(...params: never[]): Promise<unknown[]>
  draft(row: never): unknown
  write(rows: never[]): Promise<unknown>
}
export type Db = {
  Querier: { prototype: { codec: Codec; baseUrl: string } }
  relation(name: string, options: object): Rel
}

// BENCH_ROWS rows are read (at most the 2000 codec_samples.sql seeds), a quarter of that written
const READ_ROWS = Number(process.env.BENCH_ROWS ?? 2000)
const WRITE_ROWS = Math.max(1, READ_ROWS >> 2)
const WARMUP = 3
const RUNS = 15

const median = (xs: number[]) => [...xs].sort((a, b) => a - b)[xs.length >> 1] as number

// Milliseconds per call of `fn`, median of RUNS batches of `inner` calls.
async function time(fn: () => unknown | Promise<unknown>, inner = 1): Promise<number> {
  for (let i = 0; i < WARMUP; i++) await fn()
  const samples: number[] = []
  for (let i = 0; i < RUNS; i++) {
    const start = performance.now()
    for (let j = 0; j < inner; j++) await fn()
    samples.push((performance.now() - start) / inner)
  }
  return median(samples)
}

const size = (v: string | Uint8Array) =>
  typeof v === "string" ? new TextEncoder().encode(v).length : v.length

// `row(i)` is a value for each column of hotel.codec_samples, typed the way this codec's PgWire says.
export async function run(name: string, db: Db, row: (i: number) => Record<string, unknown>) {
  const proto = db.Querier.prototype
  proto.baseUrl = process.env.REL_URL as string
  const codec = proto.codec

  // Capture what the codec sees on one request of each kind.
  let decoded: string | Uint8Array | undefined
  let encoded: unknown
  const { decode, encode } = codec
  codec.decode = (body) => {
    decoded = body
    return decode(body)
  }
  codec.encode = (value) => {
    encoded = value
    return encode(value)
  }
  const reads = () =>
    db
      .relation("hotel.codec_samples", {
        select: ["*~"],
        where: ["=", ["col", "label"], "bench"],
        order_by: [["col", "id"]],
        limit: READ_ROWS,
      })
      .get()
  const samples = db.relation("hotel.codec_samples", { select: ["*~"] })
  const writes = () =>
    samples.write(
      Array.from({ length: WRITE_ROWS }, (_, i) =>
        samples.draft({ label: `bench-write-${name}`, ...row(i) } as never),
      ) as never[],
    )

  const rows = await reads()
  if (rows.length !== READ_ROWS)
    throw new Error(`expected ${READ_ROWS} bench rows, got ${rows.length}`)
  const responseBody = decoded as string | Uint8Array
  await writes()
  const requestValue = encoded
  const requestBody = encode(requestValue)
  codec.decode = decode
  codec.encode = encode

  const out = {
    codec: name,
    rows: READ_ROWS,
    "read bytes": size(responseBody),
    "decode ms": await time(() => decode(responseBody), 5),
    "read ms": await time(reads),
    "write bytes": size(requestBody),
    "encode ms": await time(() => encode(requestValue), 5),
    "write ms": await time(writes),
  }
  console.log(`BENCH ${JSON.stringify(out)}`)
}
