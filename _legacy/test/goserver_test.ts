import type { Container, ContainerInspectInfo } from "dockerode"
// import beforeShutdown from "./before-shutdown"
import { cleanupContainers, pg_container, setupContainers } from "./setup-containers"
import { beforeAll, afterAll, test, expect, describe, } from "bun:test"
// import { format } from "sql-formatter"

let BASE_URL = ""

async function _(str: string, payload: any = null) {
  const res = await fetch(`${BASE_URL}/rel2`, {
    method: "POST",
    body: payload ? JSON.stringify(payload) : undefined,
    headers: {
      "X-Query": str,
    }
  })
  const text = await res.text()

  if (res.headers.get("X-Sql-Query")) {
    const args = res.headers.get("X-Sql-Args") as string | null
    let query = res.headers.get("X-Sql-Query") as string

    if (args) {
      query = query.replaceAll(/\$1/g, `$_$ ${args} $_$`)
    }

    // console.log("\n\n --------------------- sql: \n", format(query, { language: "postgresql" }), `\n\n  (${_pg.NetworkSettings.IPAddress})\n\n`)
  }

  if (res.status !== 200) {
    throw new Error(res.statusText + " " + text)
  }
  try {
    return JSON.parse(text)
  } catch (err: any) {
    throw new Error(err!.message + " (" + text + ")")
  }
}

let _pg: ContainerInspectInfo

beforeAll(async () => {
  const { goserver, pg } = await setupContainers()
  _pg = pg
  BASE_URL = `http://${goserver.NetworkSettings.IPAddress}:3001`

  for (let i = 0; i < 30; i++) {
    if (i > 0) {
      console.log("waiting for 1 second")
      await new Promise(resolve => setTimeout(() => resolve(void 0), 1000))
    }

    try {
      console.log("checking goserver readiness")
      const res = await fetch(`${BASE_URL}/heartbeat`, {
        signal: AbortSignal.timeout(2000),
      })
      if (res.status !== 200) {
        console.log("goserver is not ready: ", res.statusText)
      } else {
        const status: { dmut: string, ok: boolean, db: string } = await res.json() as any
        if (status.dmut !== "ok" || status.db !== "ok" || !status.ok) {
          console.error("goserver has errors, not running tests")
          console.error("db: ", status.db)
          console.error("dmut: ", status.dmut)
          await cleanupContainers("on start error")
          process.exit(1)
        }
        break
      }
    } catch (err) {
      console.log("goserver is not ready: ", (err as Error).message)
    }

  }

  console.log("goserver has IP: ", goserver.NetworkSettings.IPAddress)
  console.log("postgres has IP: ", pg.NetworkSettings.IPAddress)
  console.log("connect to postgres: ", `postgres://app:app@${pg.NetworkSettings.IPAddress}:5432/app`)
})

process.on("beforeExit", code => {
  console.log("Shutting down...", code)
  // process.exit(code)
})

describe("schema", async () => {

  test("goserver replies with schema", async () => {
    const res: any = await _("GET", `${BASE_URL}/_schema`)
    expect(res).toBeObject()
    expect(res.Tables).toBeObject()
  })
})

describe("insert", async () => {

  test("seeding groups", async () => {
    const res: any = await _(`insert api.groups`, [
      { name: "admin",  description: "Administrators" },
      { name: "user",   description: "Users" },
      { name: "nobody", description: "Anonymous users" },
    ])
    expect(res).toBeArray()
    expect(res.length).toBe(3)
  })

  test("getting groups after insert", async () => {
    const res: any = await _(`api.groups`)
    expect(res).toBeArray()
    expect(res.length).toBe(3)
  })

  test("inserting recursive data", async () => {
    const res: any = await _(`api.items{ *, tags:@api.items_tags{ * } }`, [
      { id: 1, name: "item1", tags: [{tag: "tag1"}, {tag: "tag2"}] },
      { id: 2, name: "item2", tags: [{tag: "tag3"}, {tag: "tag4"}] },
    ])
    const sub = await _("GET", `${BASE_URL}/rel/api.items_tags`)
    expect(res).toBeArray()
    expect(res.length).toBe(2)
    expect(res[0].tags.length).toBe(2)
    expect(res[1].tags.length).toBe(2)
    expect(sub).toBeArray()
    expect(sub.length).toBe(4)
  })

  test("inserting recursive data with filters", async () => {
    // FIXME : should rewrite the json_table query to also apply the where clauses
    const res: any = await _(`api.items {
      *,
      tags: @items_tags { * }
    }
    where id = 1`, [
      { id: 1, name: "item6", tags: [{tag: "tag5"}] },
    ])

    expect(res).toBeArray()
    expect(res.length).toBe(1)

    // console.log(res)
    const sub = await _(`api.items_tags`)
    // console.log(sub)
    expect(sub).toBeArray()
    expect(sub.length).toBe(3)
  })

  test("inserting recursive data erroneously", async () => {
    // There is no where clause, so this will delete everything in the table.
    const res: any = await _(`api.items{ *, tags:@api.items_tags{ * } }`, [
      { id: 1, name: "item66", tags: [{tag: "tag55"}] },
    ])
    console.error(res)
    expect(res).toBeArray()
    expect(res.length).toBe(1)

    const sub = await _(`api.items_tags`)
    expect(sub).toBeArray()
    expect(sub.length).toBe(1)

    // The check should be recursive, not using directly (distinct) values.
    // This means understanding the WHERE of a parent table to add it to its sub-table.
    // Hard.

  })
  // Things to test : inserting recursive data, but with filters in a recursive relation. Then verify it only affected items that match the filter.

})

// async function main() {
//   const { goserver } = await setupContainers()

//   const goserver_uri = `http://${goserver.NetworkSettings.IPAddress}:3001`

//   await cleanupContainers("on finished")
// }

// main().finally(() => {
//   console.log("Shutting down...")
//   process.exit(0)
// })