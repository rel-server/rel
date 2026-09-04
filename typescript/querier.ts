/*!
Section 1 of `specs/typescript.md ## File layout` : the actual code — `Querier`, `relation()`, `func()`, `join()`,
`wellknown()`. This is what a developer calls directly ; it comes first in the generated file precisely so it's
the first thing seen when opening it, ahead of the developer's own schema (section 2) and the type machinery that
powers both (section 3, `query.ts`/`shapes.ts`).
*/
import type { Query, RelationQuery } from "./query"
import type { Relationships, Wellknowns } from "./schema.example"
import type {
  DefaultRow,
  Params,
  ResolveModel,
  ShapeFromQuery,
  WriteShapeFromQuery,
} from "./shapes"

// Builder for well-known queries (specs/typescript.md ## Wellknowns). `name` must be one of Wellknowns' own keys ;
// `params` is that entry's declared params object, supplied upfront here rather than deferred to .get()/.write() —
// unlike relation()/func(), a well-known query's `$param` usages are never exposed to the caller as Querier's own
// Params, hence `void` below.
export function wellknown<W extends keyof Wellknowns>(
  name: W,
  params: Wellknowns[W]["params"],
): Querier<Wellknowns[W]["shape"], Wellknowns[W]["write_shape"], void> {
  return new Querier({
    wellknown: name,
    params,
  })
}

// A known relation name resolves to its real column shape ; anything else falls back to the permissive default.
// Deliberately ONE generic signature, not two overloads : with two, a bad column on a known relation would
// silently fall through to the looser unchecked overload instead of erroring (TS only flags "no overload
// matches" when every candidate fails). Delegates to ResolveModel so root and join resolution share one path.
type ResolveRelationModel<R extends string> = ResolveModel<{ relation: R }>

// Builder for relations. `rel` must be a fully qualified "schema.relation" name.
export function relation<R extends string, const Q extends RelationQuery<ResolveRelationModel<R>>>(
  rel: R,
  request: Q,
): Querier<
  ShapeFromQuery<Q, ResolveRelationModel<R>>,
  WriteShapeFromQuery<Q, ResolveRelationModel<R>>,
  Params<Q>
> {
  const [schema, relation] = rel.split(".")
  const query = {
    ...request,
    schema,
    relation,
  }
  return new Querier(query)
}

// A known function name resolves to its Functions entry's row shape ; same single-generic-signature reasoning as
// ResolveRelationModel above. RelationQuery's Rel must extend object — meaningful for a set-returning function
// (select/join operate on its `relation`) but not for a scalar one (`returns: number`, say) ; falls back to the
// permissive default there, same as an unrecognized name. A scalar function is better called via an Expression's
// `["call", ...]` tag than through func()'s select/join machinery anyway.
type ResolveCalledFunctionModel<F extends string> =
  ResolveModel<{ function: F }> extends infer M extends object ? M : DefaultRow

// Builder for functions. `fn` must be a fully qualified "schema.function" name. `function` can't be used as an
// identifier (reserved word), hence `func`.
export function func<
  F extends string,
  const Q extends RelationQuery<ResolveCalledFunctionModel<F>>,
>(
  fn: F,
  request: Q,
): Querier<
  ShapeFromQuery<Q, ResolveCalledFunctionModel<F>>,
  WriteShapeFromQuery<Q, ResolveCalledFunctionModel<F>>,
  Params<Q>
> {
  const [schema, fn_name] = fn.split(".")
  const query = {
    ...request,
    schema,
    function: fn_name,
  }
  return new Querier(query)
}

// Splits a `Relationships` shortcut string ("schema.relation<dir>;referenced_col:referencing_col[,...]") into its
// `on` clause plus target schema/relation. `dir` is `>` when the current relation owns the FK (outgoing — the pair
// reads target_col:source_col as-is) or `<` when the joined relation owns it (incoming — the pair must be
// reversed, since it's always written referenced_col:referencing_col regardless of direction).
function parseShortcut(shortcut: string): {
  schema: string
  relation: string
  on: { [column: string]: string }
} {
  const dir = shortcut.includes(">") ? ">" : "<"
  const [relPart, pairsPart] = shortcut.split(";")
  const [schema, relation] = relPart.slice(0, -1).split(".")
  const on: { [column: string]: string } = {}
  for (const pair of pairsPart.split(",")) {
    const [referenced_col, referencing_col] = pair.split(":")
    if (dir === ">") {
      on[referenced_col] = referencing_col
    } else {
      on[referencing_col] = referenced_col
    }
  }
  return { schema, relation, on }
}

// Builder for joins. `key` narrows to one owning relation's relationship variants (`Relationships[key]`, possibly
// a union — one member per FK reachable from that relation) ; `shortcut` then picks exactly one member, so a
// relation with several FKs never collides on a single shape. `shortcut` is parsed directly into `on`/`relation`/
// `schema`, and kept on the returned object's own type so shapes.ts's JoinCardinality/ResolveModel can resolve
// this join's row shape and cardinality from it alone, without needing `key` again.
export function join<
  K extends keyof Relationships,
  S extends Relationships[K]["shortcut"],
  const Q extends RelationQuery<Extract<Relationships[K], { shortcut: S }>["relation"]>,
>(_key: K, shortcut: S, request: Q): Q & { shortcut: S } {
  const { schema, relation, on } = parseShortcut(shortcut)
  return { ...request, schema, relation, on, shortcut } as Q & { shortcut: S }
}

// Both wellknown() and relation() produce a Querier with its Shape/WriteShape/Params known through their own
// return types. WriteShape defaults to Shape so a hand-built Querier (or wellknown(), which doesn't compute one)
// still works ; relation()/func() always supply the real, narrower WriteShapeFromQuery explicitly.
export class Querier<Shape = unknown, WriteShape = Shape, Params = void> {
  public query: Query

  constructor(
    query: Query,
    public has_params = false,
  ) {
    this.query = Querier.stripShortcut(query) as Query
  }

  // `shortcut` is join()'s client-side sugar (schema.example.ts's Relationships lookup) — never part of the wire
  // format. Stripped once here, on construction, rather than on every doQuery() call.
  private static stripShortcut(obj: unknown): unknown {
    if (obj == null || typeof obj !== "object") {
      return obj
    }
    if (Array.isArray(obj)) {
      return obj.map((item) => Querier.stripShortcut(item))
    }
    const { shortcut, ...rest } = obj as { shortcut?: unknown; [name: string]: unknown }
    for (const key of Object.keys(rest)) {
      rest[key] = Querier.stripShortcut(rest[key])
    }
    return rest
  }

  private doParams(obj: unknown, params: { [name: string]: unknown }): unknown {
    if (obj == null) {
      return obj
    }
    let diff = false
    if (Array.isArray(obj)) {
      if (obj[0] === "$param") {
        // FIXME maybe do some checking if a type was supplied in obj[2]
        const key = obj[1]
        if (typeof key !== "string") {
          throw new Error("find a better error name") // do that claude
        }
        return params[key]
      }
      const res = new Array(obj.length)
      for (let i = 0, l = obj.length; i < l; i++) {
        res[i] = this.doParams(obj[i], params)
        diff = diff || res[i] !== obj[i]
      }
      return diff ? res : obj
    }
    if (typeof obj === "object") {
      const res: { [name: string]: unknown } = {}
      const _obj = obj as { [name: string]: unknown }
      for (const x of Object.getOwnPropertyNames(obj)) {
        const orig = _obj[x]
        const r = this.doParams(orig, params)
        res[x] = r
        diff = diff || r !== orig
      }
      return diff ? res : obj
    }
    return obj
  }

  // Substitutes every `$param` node in `this.query` with its value from `params`, via doParams above ; a query
  // with no `$param` usage at all (Params = void) has nothing to substitute, so `params` is optional there.
  private doQuery(params: Params): Query {
    return this.doParams(this.query, (params ?? {}) as { [name: string]: unknown }) as Query
  }

  // Explicit Promise<Shape> return type : _send() only returns Promise<any> (res.json() can't know what it
  // parsed), so without this annotation Shape would be silently erased. write()'s overloads do the same.
  get(_params: Params): Promise<Shape> {
    return this._send(this.doQuery(_params))
  }

  // send a write request to rel. `data` is WriteShape, not Shape — get/set/get-set's read-vs-write asymmetry
  // (shapes.ts's WriteShapeFromQuery doc comment) means these genuinely differ whenever a query uses them.
  write(_params: Params, data: WriteShape): Promise<Shape>
  write(data: WriteShape): Promise<Shape>
  write(_params: Params | WriteShape, _data?: WriteShape): Promise<Shape> {
    const params = _data != null ? (_params as Params) : (void 0 as Params)
    const data = _data != null ? _data : (_params as WriteShape)
    return this._send({ query: this.doQuery(params), data })
  }

  private _send(query: unknown) {
    return fetch("/rel", {
      credentials: "include",
      method: "POST",
      body: JSON.stringify(query),
    }).then((res) => res.json())
  }
}
