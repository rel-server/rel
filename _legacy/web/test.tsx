import { CodeJar } from "codejar"
import { $class, $click, $observe, $scrollable, css, If, node_append, node_observe, node_remove, o, Repeat, type Renderable } from "elt"
import Prism from "prismjs"
import { format } from "sql-formatter"

import css_text from "prismjs/themes/prism-twilight.css"
css`${css_text}`

import * as json from "./json.style"

css`
[class*="language-"] {
  white-space: pre-wrap !important;
}
`

import "prismjs/components/prism-sql"


import * as I from "elt-fa/solid"
import * as cls from "./test.style"

function $highlight_sql(sql: o.RO<string | null>) {
  return function $highlight_sql(node: HTMLElement) {
    node_observe(node, sql, (sql) => {
      const highlighted = Prism.highlight(sql ?? "", Prism.languages.sql!, "sql")
      node.innerHTML = highlighted
    })
  }
}

const o_query = o("")
const o_json_value = o(null as unknown) as o.Observable<unknown>
const o_history = o(new Set<string>())
const o_error = o<Node | null>(null)
const o_sql = o<string | null>(null)
const QUERY_HISTORY_KEY = "_query_history"

function JsonElt(value: unknown, { depth, maxDepth }: { depth: number, maxDepth: number } = { depth: 0, maxDepth: 2 }): Renderable {
  // Observable for collapsed state
  if (value === null || value === undefined || value === true || value === false) {
    return <span class={json.constant}>{""+value}</span>
  }

  if (typeof value === "number") {
    return <span class={json.number}>{value}</span>
  }

  if (typeof value === "string") {
    return <span class={json.string}>"{value.replace(/"/g, "\\\"")}"</span>
  }

  const is_array = Array.isArray(value)
  let iterable = [...(is_array ? value.entries() : Object.entries(value))]
  let open = is_array ? "[" : "{"
  let close = is_array ? "]" : "}"
  const o_collapsed = o(depth >= maxDepth || iterable.length === 0)

  function toggle(ev: MouseEvent) {
    if (ev.ctrlKey) {
      ev.stopPropagation()
      ev.preventDefault()
      const str = JSON.stringify(value, null, 2)

      navigator.clipboard.write([new ClipboardItem({
        "text/plain": new Blob([str], { type: "text/plain" }),
      })])

      //📋
      const copied = <div class={json.copied}>📋 Copied !</div> as HTMLDivElement
      node_append(ev.target as Node, copied)

      copied.animate([{opacity: 1, transform: "translateY(0)"}, {opacity: 0, transform: "translateY(-30%)"}], {
        duration: 450,
        easing: "ease-in",
        fill: "forwards"
      }).addEventListener("finish", () => {
        node_remove(copied)
      })

      return
    }
    o_collapsed.mutate(e => !e)
  }

  return o_collapsed.tf(c =>
    c ? <span class={[json.punctuation, json.clickable]}>
      {$click(toggle)}
      {open}{iterable.length > 0 ? "… (" + iterable.length + ")" : ""}{close}
    </span>
    : <>
    <span class={[json.punctuation, json.clickable]}>
      {$click(toggle)}
      {open}
    </span>
    <div class={json.container}>
      {iterable.map(([k, v]) => <div>
        {typeof k === "string" ? <span class={json.property}>{k}: </span> : <span class={json.punctuation}>- </span>}{JsonElt(v, { depth: depth+1, maxDepth })}
      </div>)}
    </div>
    <div class={json.punctuation}>{close}</div>
    </>
  )
}

try {
  const history: string[] = JSON.parse(localStorage.getItem(QUERY_HISTORY_KEY) || "[]")
  if (!Array.isArray(history)) {
    throw new Error("Invalid history")
  }
  o_history.set(new Set(history))
  o_query.set(history[history.length - 1] ?? "")
} catch (e) {
  o_history.set(new Set())
}

//
function query() {

  o_history.mutate(history => {
    const q = o.get(o_query)
    const _history = [...history].slice(-49)
    history = new Set(_history)
    history.delete(q)
    history.add(q)
    return history
  })

  o_sql.set(null)
  o_json_value.set(null)
  const query = o.get(o_query).replaceAll("\\", "\\\\").replaceAll("\n", "\\n")

  fetch("/rel", {
    method: "GET",
    headers: {
      "X-Reply-Sql": "true",
      "X-Query": query,
    }
  }).then(res => {
    return res.text()
    // console.log(res)
  }).then(sql => {
    sql = format(sql, { language: "postgresql" })
    o_sql.set(sql)
  })

  const form = new FormData()
  form.set("query", query)
  form.set("payload", "")

  fetch("/rel", {
    method: "POST",
    credentials: "include",
    body: form,
  }).then(res => {
    if (res.status !== 200) {
      return Promise.reject(res)
    }

    return res.json()
  }).then(data => {
    o_json_value.set(data)
    o_error.set(null)
  }).catch(async (err: Response) => {

    if (err instanceof Response) {
      const data = await err.text()
      // extract all between <body> and </body>
      const body = data.match(/<body>(.*?)<\/body>/s)?.[1]
      const sql = err.headers.get("X-Sql-Query") ?? ""
      o_sql.set(sql)
      const error_div =
        <div class="error">
          {node => {
            node.innerHTML = ""+(body??data)
          }}
        </div>

      o_error.set(error_div)
    } else {
      const err_ = err as any
      console.error(err)

      o_error.set(<div style={{whiteSpace: "pre-wrap"}} class={[cls.error, "mono"]}>Error: {err_?.message + "\n"}{err_?.stack}
      {"\n\n"}
      <pre class="language-sql"><code>{$highlight_sql(o_sql)}</code></pre>
      </div>)
    }
  })
}

document.addEventListener("keydown", (e) => {
  if (e.key === "Enter" && e.ctrlKey) {
    query()
  }
})

function show_history() {
  show(fut => <sl-dialog class={cls.dialog}>
    {$scrollable}
    <div slot="label">History</div>
    <e-flex column>
      {Repeat(o_history.tf(history => Array.from(history)), o_hist => <e-flex class={cls.clickable}>
        <e-box tabindex={0} grow pad="small"  class={cls.clickable}>
          {$click(e => {
            o_query.set(o.get(o_hist))
            fut.resolve()
          })}
          {o_hist}
        </e-box>
        <e-flex tabindex={0} align="center" pad="small" class={cls.clickable}>
          {$click(e => {
            o_history.mutate(history => {
              const h = new Set(history)
              h.delete(o.get(o_hist))
              return h
            })
          })}
          <I.FaXmark/>
        </e-flex>
      </e-flex>)}
    </e-flex>
  </sl-dialog>)
}

node_append(document.body, $class(theme.classes.theme))
node_append(document.body, <e-flex column nowrap pad gap style="height: 100%;">
  {$observe(o_history, hist => {
    localStorage.setItem(QUERY_HISTORY_KEY, JSON.stringify(Array.from(hist)))
  })}
  <e-flex align="baseline" gap>
    <sl-button size="small">
      <I.FaClock slot="prefix"/> History
      {$click(e => {
        show_history()
      })}

    </sl-button>
    <sl-button size="small">
      {$click(query)}
      {$tooltip("Ctrl+Enter")}
      Query
      <I.FaDatabase slot="suffix"/>
    </sl-button>
  </e-flex>
  <sl-split-panel position-in-pixels={250} primary="start" vertical style="flex-grow: 1; flex-basis: 0; width: 100%; overflow: hidden;">
    <pre slot="start" class={["mono", "editor"]}><code class="language-sql">
      {node => {

        const lock = o.exclusive_lock()
        node.addEventListener("keydown", e => {
          if (e.key === "Enter" && e.ctrlKey) {
            query()
            e.preventDefault()
            e.stopPropagation()
          }
        })

        const jar = CodeJar(node, (elt, pos) => {
          elt.innerHTML = Prism.highlight(elt.textContent ?? "", Prism.languages.sql!, "sql")
        }, {
          tab: "  ",
          indentOn: /[\(\[\{]/,
          // moveToNewLine: /[\(\[\{]/,
          preserveIdent: true,
          spellcheck: false,
          catchTab: true,
        })

        jar.onUpdate(code => {
          lock(() => {
            o_query.set(code)
          })
        })

        setTimeout(() => {
          node.focus()

          node_observe(node, o_query, (code) => {
            lock(() => {
              jar.updateCode(o.get(o_query))
            })
          })
        })
      }}
    </code></pre>
    {/* <sl-textarea class="mono" slot="start">
      {$connected(node => {
        setTimeout(() => {
          // lit-element is not synchronous, so even when connected the input does not exist yet.
          node.focus()
        }, 5)
      })}
      {$model(o_query)}
    </sl-textarea> */}
    <sl-tab-group slot="end" style="flex: 1; min-height: 0;">
      <sl-tab slot="nav" panel="json">
        {$observe(o_error, (err, _, node) => {
          if (err == null) {
            node.active = true
          }
        })}
        JSON
      </sl-tab>
      <sl-tab slot="nav" panel="sql">SQL</sl-tab>
      <sl-tab-panel name="json" style="flex: 1; min-height: 0; overflow: hidden;">
        <div>
          {o_json_value.tf(json => JsonElt(json))}
        </div>
      </sl-tab-panel>
      <sl-tab-panel name="sql">
        <sl-button size="small">
          {$click(() => {
            navigator.clipboard.write([new ClipboardItem({
              "text/plain": new Blob([o.get(o_sql) ?? ""], { type: "text/plain" }),
            })])
          })}
          <I.FaCopy slot="prefix"/> Copy
        </sl-button>

        {o_sql.tf(sql => <pre class="language-sql"><code>{$highlight_sql(sql)}</code></pre>)}
      </sl-tab-panel>

      {If(o_error, () => <>
        <sl-tab slot="nav" panel="error">
          {$observe(o_error, (err, _, node) => {
            node.active = true
          })}
          Error
        </sl-tab>
        <sl-tab-panel name="error">
          {o_error}
        </sl-tab-panel>
      </>)}
    </sl-tab-group>
    {/* {If(o_error, error => <e-box style="overflow: scroll;" grow class={cls.error} slot="end">
      <div>
        {$scrollable}
        {error}
        {o_json_value.tf(json => JsonElt(json))}

      </div>
    </e-box>)} */}
  </sl-split-panel>
</e-flex>)

css`
sl-tab-group::part(body) {

}

sl-tab-group::part(base) {
  height: 100%;
}
`