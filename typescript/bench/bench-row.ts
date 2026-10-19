// The row of the benchmark for a codec whose dates and bytea are strings.
export const plainRow = (i: number): Record<string, unknown> => ({
  taken_tz: new Date(1_700_000_000_000 + i * 60_000).toISOString(),
  taken_at: new Date(1_700_000_000_000 + i * 60_000).toISOString().slice(0, 19),
  taken_on: new Date(Date.UTC(2026, 0, 1 + i)).toISOString().slice(0, 10),
  content: `\\x${"ab".repeat(32)}`,
  big: 5_000_000_000 + i,
  price: i * 1.125,
})
