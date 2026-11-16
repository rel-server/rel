/*!
The actual code — `Querier`, `relation()`, `func()`, `join()`, `wellknown()`. Placed right after pg_values.ts in
the generated file, ahead of the schema and the type machinery.
*/
import type { Query, RelationQuery } from "./query"
import type {
  ComputedProperties,
  Functions,
  IneligibleRelationships,
  Relationships,
  Wellknowns,
} from "./schema.example"
import type {
  DefaultRow,
  DeferredFunctionArgs,
  FunctionArgs,
  FunctionName,
  MatchOverload,
  Params,
  ParsedShortcut,
  Prettify,
  RelationName,
  ResolveModel,
  RootShapeFromFunctionMember,
  ShapeFromQuery,
  WithProto,
  WriteDataFromQuery,
  WriteShapeFromQuery,
  DraftShapeFromQuery,
  WriteRowOf,
  DraftResult,
  DraftStrict,
  JoinPaths,
  RowAtPath,
} from "./shapes"

/**
 Builder for well-known queries. `params` is supplied here, not to .get()/.write(), so the Querier's Params is `void`.
 */
export function wellknown<W extends keyof Wellknowns>(
  name: W,
  params: Wellknowns[W]["params"],
): Querier<Wellknowns[W]["shape"], Wellknowns[W]["write_shape"], void> {
  return new Querier({
    wellknown: name,
    params,
  })
}

type ResolveRelationModel<R extends string> = ResolveModel<{ relation: R }>

// K is unconstrained because it is also fed a shortcut's parsed target name ; an unknown one gives `never`, making
// the scoped join uncallable.
type SafeRelationships<K extends string> = K extends keyof Relationships ? Relationships[K] : never

// The foreign keys of K that can't be joined (unindexed columns). Typing one is still an error, but one that says why.
type SafeIneligible<K extends string> = K extends keyof IneligibleRelationships
  ? IneligibleRelationships[K]
  : never

// Includes the ineligible shortcuts so ShortcutCheck can reject them with their reason.
type JoinShortcut<K extends string> =
  | SafeRelationships<K>["shortcut"]
  | SafeIneligible<K>["shortcut"]

// Intersected onto the `shortcut` parameter : for an ineligible one, an object the string can't satisfy, whose
// `error` shows up in the compiler's message.
type ShortcutCheck<K extends string, S> = S extends SafeIneligible<K>["shortcut"]
  ? { error: Extract<SafeIneligible<K>, { shortcut: S }>["reason"] }
  : unknown

// Reads Join back off Q itself (`const Q extends RelationQuery<Rel, JoinOf<Q>>`) : left unsupplied, Join defaults to
// `{ [name: string]: RelationQuery }`, which made select/where accept any string. `{}` is needed for
// "no join" since its keyof is `never` ; biome's suggested replacements have keyof `string`.
type JoinOf<Q> = Q extends { join: infer J extends { [name: string]: RelationQuery } }
  ? J
  : // biome-ignore lint/complexity/noBannedTypes: see above — `{}` is deliberate here, not a placeholder
    {}

// Computed fields are referable like columns, so they widen the Rel checked by the Q constraint only, never the Rel
// of the result shape : "*"/"*~" must not pull a computed field in.
type WithComputedKeys<Rel extends object, R extends string> = R extends keyof ComputedProperties
  ? Rel & { [K in keyof ComputedProperties[R]]?: unknown }
  : Rel

// "hotel.rooms*id:property_id" -> "hotel.rooms" ; lets a nested join's callback scope itself to its target.
type TargetRelationName<S extends string> = ParsedShortcut<S>["target"]

/**
 `join()` scoped to relation K, so a nested join never repeats the relation it joins from.
 */
// Three call signatures, not a union parameter : `proto`'s `this` is only typed against a single, non-union
// parameter type. The callback form returns `WithProto<Q, Rel>` for the same reason.
export type ScopedJoin<K extends string> = {
  <
    S extends JoinShortcut<K>,
    const Q extends RelationQuery<
      WithComputedKeys<
        Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
        TargetRelationName<S>
      >,
      JoinOf<Q>
    > & { join?: ValidatedJoin<TargetRelationName<S>, JoinOf<Q>> },
  >(
    shortcut: S & ShortcutCheck<K, S>,
    request?: WithProto<Q, Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
  ): Q & { shortcut: S }
  <
    S extends JoinShortcut<K>,
    const Q extends RelationQuery<
      WithComputedKeys<
        Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
        TargetRelationName<S>
      >,
      JoinOf<Q>
    >,
  >(
    shortcut: S & ShortcutCheck<K, S>,
    request: Querier<unknown, unknown, unknown, Q>,
  ): Q & { shortcut: S }
  <
    S extends JoinShortcut<K>,
    const Q extends RelationQuery<
      WithComputedKeys<
        Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
        TargetRelationName<S>
      >,
      JoinOf<Q>
    >,
  >(
    shortcut: S & ShortcutCheck<K, S>,
    request: (
      join: ScopedJoin<TargetRelationName<S>>,
    ) => WithProto<Q, Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
  ): Q & { shortcut: S }
}

// Checks `join` entries written as object literals with a `shortcut` (no join() call), recursively, the way join()
// checks its argument. A mismatch is reported at the enclosing key, not the leaf field. An entry without `shortcut`
// (raw on/relation/schema form) is not checked.
type ValidatedJoin<K extends string, J> = {
  [A in keyof J]: J[A] extends { shortcut: infer S extends string }
    ? S extends SafeRelationships<K>["shortcut"]
      ? J[A] extends RelationQuery<
          WithComputedKeys<
            Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
            TargetRelationName<S>
          >,
          JoinOf<J[A]>
        >
        ? J[A] & { join?: ValidatedJoin<TargetRelationName<S>, JoinOf<J[A]>> }
        : RelationQuery<
            WithComputedKeys<
              Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
              TargetRelationName<S>
            >,
            JoinOf<J[A]>
          > & { shortcut: S }
      : S extends SafeIneligible<K>["shortcut"]
        ? { shortcut: S & ShortcutCheck<K, S> }
        : { shortcut: SafeRelationships<K>["shortcut"] }
    : J[A]
}

// The join handed to a relation()/join() callback, scoped to `key`.
function scopedJoin<K extends string>(key: K): ScopedJoin<K> {
  return ((shortcut: string, request?: unknown) =>
    join(key as never, shortcut as never, request as never)) as ScopedJoin<K>
}

// A callback is invoked with a join scoped to `key` ; an absent request is `{}` (select all).
function resolveRequest<K extends string, Q>(
  key: K,
  request: Q | ((join: ScopedJoin<K>) => Q) | undefined,
): Q {
  if (typeof request === "function") {
    return (request as (join: ScopedJoin<K>) => Q)(scopedJoin(key))
  }
  return request ?? ({} as Q)
}

// Adds the `schema`/`relation` that relation() spreads onto the query at runtime, so the shape types can resolve the
// model from Q alone.
type IdentifiedQuery<Q, R extends string> = R extends `${infer Sc}.${infer Rel}`
  ? Q & { schema: Sc; relation: Rel }
  : Q

// A relation root reads as an array of rows. Q is unconstrained : relation() already validated it.
type RelationQuerier<R extends RelationName, Q> = Querier<
  ShapeFromQuery<IdentifiedQuery<Q, R>, ResolveRelationModel<R>>[],
  WriteDataFromQuery<IdentifiedQuery<Q, R>, ResolveRelationModel<R>>,
  Params<Q>,
  IdentifiedQuery<Q, R>,
  DraftShapeFromQuery<IdentifiedQuery<Q, R>, ResolveRelationModel<R>>[]
>

/**
 Builder for relations. `rel` is "schema.relation". `request` defaults to select all, and can be a query object or
 a callback receiving a `join` already scoped to `rel`.
 */
export function relation<
  R extends RelationName,
  const Q extends RelationQuery<WithComputedKeys<ResolveRelationModel<R>, R>, JoinOf<Q>> & {
    join?: ValidatedJoin<R, JoinOf<Q>>
  },
>(rel: R, request?: WithProto<Q, ResolveRelationModel<R>>): RelationQuerier<R, Q>
export function relation<
  R extends RelationName,
  const Q extends RelationQuery<WithComputedKeys<ResolveRelationModel<R>, R>, JoinOf<Q>> & {
    join?: ValidatedJoin<R, JoinOf<Q>>
  },
>(
  rel: R,
  request: (join: ScopedJoin<R>) => WithProto<Q, ResolveRelationModel<R>>,
): RelationQuerier<R, Q>
export function relation<
  R extends RelationName,
  const Q extends RelationQuery<WithComputedKeys<ResolveRelationModel<R>, R>, JoinOf<Q>> & {
    join?: ValidatedJoin<R, JoinOf<Q>>
  },
>(
  rel: R,
  request?: WithProto<Q, ResolveRelationModel<R>> | ((join: ScopedJoin<R>) => Q),
): RelationQuerier<R, Q> {
  const [schema, relation] = rel.split(".")
  const query = {
    ...resolveRequest(rel, request),
    schema,
    relation,
  } as IdentifiedQuery<Q, R>
  return new Querier(query)
}

// A scalar function's model is not an object, so it falls back to the permissive DefaultRow.
type ResolveCalledFunctionModel<F extends string> =
  ResolveModel<{ function: F }> extends infer M extends object ? M : DefaultRow

/**
 Builder for functions. `fn` is "schema.function". `request.arguments` is checked against the function's declared
 arguments and may use `$param`.
 */
export function func<
  F extends FunctionName,
  const Q extends RelationQuery<
    ResolveCalledFunctionModel<F>,
    JoinOf<Q>,
    DeferredFunctionArgs<Functions[F]>
  >,
>(
  fn: F,
  request: WithProto<Q, ResolveCalledFunctionModel<F>>,
): Querier<
  // An array of rows for a set-returning overload ; the bare value for a scalar one, the only root that isn't an array.
  RootShapeFromFunctionMember<F extends keyof Functions ? Functions[F] : never, Q>,
  WriteShapeFromQuery<Q, ResolveCalledFunctionModel<F>>,
  Params<Q>,
  Q,
  DraftShapeFromQuery<Q, ResolveCalledFunctionModel<F>>
> {
  const [schema, fn_name] = fn.split(".")
  const query = {
    ...request,
    schema,
    function: fn_name,
  } as Q
  return new Querier(query)
}

/**
 Calls `fn` right away and resolves to its return value : a scalar for a scalar overload, an array of rows for a
 set-returning one. `args` must match one overload exactly (plain values, no `$param`) ; the return type is that
 overload's.
 */
export function call<F extends FunctionName, const Args extends FunctionArgs<Functions[F]>>(
  fn: F,
  args: Args,
): Promise<RootShapeFromFunctionMember<MatchOverload<Functions[F], Args>, Record<string, never>>> {
  const [schema, fn_name] = fn.split(".")
  const query = {
    schema,
    function: fn_name,
    arguments: args,
  } as Query
  return new Querier<
    RootShapeFromFunctionMember<MatchOverload<Functions[F], Args>, Record<string, never>>
  >(query).get()
}

// Parses "schema.relation<marker>referenced_col:referencing_col[,...]". `>` is outgoing (this relation owns the
// foreign key) ; `<` and `*` are incoming, so the pair is reversed. `<`/`*` differ only in cardinality.
function parseShortcut(shortcut: string): {
  schema: string
  relation: string
  on: { [column: string]: string }
} {
  const markerIndex = shortcut.search(/[<>*]/)
  const marker = shortcut[markerIndex]
  const [schema, relation] = shortcut.slice(0, markerIndex).split(".")
  const on: { [column: string]: string } = {}
  for (const pair of shortcut.slice(markerIndex + 1).split(",")) {
    const [referenced_col, referencing_col] = pair.split(":")
    if (marker === ">") {
      on[referenced_col] = referencing_col
    } else {
      on[referencing_col] = referenced_col
    }
  }
  return { schema, relation, on }
}

/**
 Builder for joins from relation `key`. `shortcut` picks one foreign key and gives `on`/`schema`/`relation`, whatever
 form `request` takes. `request` is optional and can be a query object, a callback receiving a join scoped to the
 shortcut's target, or a Querier from relation()/func() to reuse its `select`/`proto`.
 */
export function join<
  K extends keyof Relationships | keyof IneligibleRelationships,
  S extends JoinShortcut<K>,
  const Q extends RelationQuery<
    WithComputedKeys<
      Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
      TargetRelationName<S>
    >,
    JoinOf<Q>
  > & { join?: ValidatedJoin<TargetRelationName<S>, JoinOf<Q>> },
>(
  key: K,
  shortcut: S & ShortcutCheck<K, S>,
  request?: WithProto<Q, Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
): Q & { shortcut: S }
export function join<
  K extends keyof Relationships | keyof IneligibleRelationships,
  S extends JoinShortcut<K>,
  const Q extends RelationQuery<
    WithComputedKeys<
      Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
      TargetRelationName<S>
    >,
    JoinOf<Q>
  >,
>(
  key: K,
  shortcut: S & ShortcutCheck<K, S>,
  request: Querier<unknown, unknown, unknown, Q>,
): Q & { shortcut: S }
export function join<
  K extends keyof Relationships | keyof IneligibleRelationships,
  S extends JoinShortcut<K>,
  const Q extends RelationQuery<
    WithComputedKeys<
      Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
      TargetRelationName<S>
    >,
    JoinOf<Q>
  >,
>(
  key: K,
  shortcut: S & ShortcutCheck<K, S>,
  request: (
    join: ScopedJoin<TargetRelationName<S>>,
  ) => WithProto<Q, Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
): Q & { shortcut: S }
export function join<
  K extends keyof Relationships | keyof IneligibleRelationships,
  S extends JoinShortcut<K>,
  const Q extends RelationQuery<
    WithComputedKeys<
      Extract<SafeRelationships<K>, { shortcut: S }>["relation"],
      TargetRelationName<S>
    >,
    JoinOf<Q>
  >,
>(
  _key: K,
  shortcut: S,
  request?:
    | WithProto<Q, Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>
    | Querier<unknown, unknown, unknown, Q>
    | ((join: ScopedJoin<TargetRelationName<S>>) => Q),
): Q & { shortcut: S } {
  const { schema, relation, on } = parseShortcut(shortcut)
  const target = `${schema}.${relation}` as TargetRelationName<S>
  const resolved = request instanceof Querier ? request.query : resolveRequest(target, request)
  return {
    ...resolved,
    schema,
    relation,
    on,
    shortcut,
  } as Q & { shortcut: S }
}

// applyProto walks the query's join tree alongside the response, reading cardinality off the value itself.
type ProtoQuery = {
  proto?: { [name: string]: ((...args: unknown[]) => unknown) | PropertyDescriptor }
  join?: { [name: string]: ProtoQuery }
}

// One prototype per `proto` object, not per row.
const protoCache = new WeakMap<object, object>()

function buildPrototype(proto: {
  [name: string]: ((...args: unknown[]) => unknown) | PropertyDescriptor
}): object {
  const cached = protoCache.get(proto)
  if (cached) {
    return cached
  }
  const descriptors: PropertyDescriptorMap = {}
  for (const key of Object.keys(proto)) {
    // Read the descriptor, not `proto[key]`, which would invoke an inline getter. An entry is an inline accessor, a
    // method, or an accessor helper's descriptor.
    const ownDesc = Object.getOwnPropertyDescriptor(proto, key) as PropertyDescriptor
    if (ownDesc.get || ownDesc.set) {
      descriptors[key] = ownDesc
    } else if (typeof ownDesc.value === "function") {
      descriptors[key] = { value: ownDesc.value, enumerable: true }
    } else {
      descriptors[key] = ownDesc.value as PropertyDescriptor
    }
  }
  const prototype = Object.defineProperties({}, descriptors)
  protoCache.set(proto, prototype)
  return prototype
}

function applyProto(value: unknown, query: ProtoQuery): unknown {
  if (value == null || typeof value !== "object") {
    return value
  }
  if (Array.isArray(value)) {
    for (const item of value) {
      applyProto(item, query)
    }
    return value
  }
  const join = query.join
  if (join) {
    for (const key of Object.keys(join)) {
      applyProto((value as { [name: string]: unknown })[key], join[key])
    }
  }
  if (query.proto) {
    Object.setPrototypeOf(value, buildPrototype(query.proto))
  }
  return value
}

/**
 The argument list of get(): the params object is required when the query has `$param`s, and may be left out when
 it has none (`Params` is then `unknown`, or `void` for a hand-built Querier).
 */
type OptionalWhenNoParams<P> = unknown extends P ? [params?: P] : [params: P]

/**
 How requests are written and responses read : JSON text by default. `contentType` is sent as both `Content-Type` and
 `Accept` when set, so a codec for another format (CBOR : "application/cbor") needs nothing else ; left out, neither
 header is sent and the server answers JSON. `accept` is sent as `Accept` instead of `contentType` when the answer
 is to be asked for differently from how the request is written. `binary` says `decode` takes the response bytes
 rather than its text. Errors are always JSON text whatever was asked, so they never reach `decode`.
 */
export interface Codec {
  contentType?: string
  accept?: string
  binary?: boolean
  encode(value: unknown, options?: EncodeOptions): string | Uint8Array
  decode(body: string | Uint8Array): unknown
}

/**
 What a query tells its codec about how to write a request. A codec that has no use for an option ignores it.
 */
export interface EncodeOptions {
  /** The query's `sendStructures` : whether objects that share their keys may be written once and referred to. */
  structures?: boolean
}

/**
 A query, built by relation()/func()/wellknown() with its types filled in. For a hand-built Querier, WriteShape
 defaults to Shape and Q to `Query`.
 */
export class Querier<
  Shape = unknown,
  WriteShape = Shape,
  Params = void,
  Q = Query,
  DraftShape = WriteShape,
> {
  public query: Q

  // get()/write() POST to `${baseUrl}/rel`. Set `Querier.prototype.baseUrl` for every query, or `baseUrl` on one
  // query. Read at call time.
  declare baseUrl: string

  // JSON unless replaced : `Querier.prototype.codec` for every query, or `codec` on one. A codec comes with the
  // `PgWire` types it implies.
  declare codec: Codec

  // Whether a codec that can (CBOR) writes objects sharing their keys as structures. Set on the prototype or on one
  // query ; a query built from it (join(), ...) starts from the prototype's again. Turn off for bodies whose objects
  // rarely share keys (a `jsonb` dictionary). Responses are not affected.
  declare sendStructures: boolean

  // Type-only, for `typeof q.$row` / `typeof q.$draft` ; `undefined` at runtime. `$row` is one row as get() returns
  // it, `$draft` one row as draft() returns it.
  declare readonly $row: Prettify<WriteRowOf<Shape>>
  declare readonly $draft: Prettify<WriteRowOf<DraftShape>>

  // `query` without `proto`, built once ; `hasParam` lets doQuery skip substitution.
  private readonly wireQuery: unknown
  private readonly hasParam: boolean

  constructor(
    query: Q,
    public has_params = false,
  ) {
    const expanded = Querier.stripShortcut(query)
    this.query = expanded as Q
    const normalized = Querier.normalizeForWire(expanded)
    this.wireQuery = normalized.value
    this.hasParam = normalized.hasParam
  }

  // `shortcut` is client-side only. A join written as an object literal never went through join(), so its shortcut
  // is expanded here when `on` is absent.
  private static stripShortcut(obj: unknown): unknown {
    if (obj == null || typeof obj !== "object") {
      return obj
    }
    if (Array.isArray(obj)) {
      return obj.map((item) => Querier.stripShortcut(item))
    }
    const { shortcut, ...rest } = obj as {
      shortcut?: string
      on?: unknown
      [name: string]: unknown
    }
    if (typeof shortcut === "string" && rest.on == null) {
      Object.assign(rest, parseShortcut(shortcut))
    }
    for (const key of Object.keys(rest)) {
      // Not walked : that would invoke its getters. Kept by reference.
      if (key === "proto") {
        continue
      }
      rest[key] = Querier.stripShortcut(rest[key])
    }
    return rest
  }

  // Drops `proto` and reports whether any `$param` was found.
  private static normalizeForWire(obj: unknown): { value: unknown; hasParam: boolean } {
    if (obj == null || typeof obj !== "object") {
      return { value: obj, hasParam: false }
    }
    if (Array.isArray(obj)) {
      if (obj[0] === "$param") {
        return { value: obj, hasParam: true }
      }
      let hasParam = false
      const value = obj.map((item) => {
        const r = Querier.normalizeForWire(item)
        hasParam = hasParam || r.hasParam
        return r.value
      })
      return { value, hasParam }
    }
    let hasParam = false
    const value: { [name: string]: unknown } = {}
    for (const key of Object.keys(obj)) {
      if (key === "proto") {
        continue
      }
      const r = Querier.normalizeForWire((obj as { [name: string]: unknown })[key])
      value[key] = r.value
      hasParam = hasParam || r.hasParam
    }
    return { value, hasParam }
  }

  // Checks one row against the write shape (every required column and join present, no `proto` keys) and attaches
  // the `proto` of this query and its joins, in place ; pass the result inside `[...]` to write(). Optional keys
  // `data` supplied come back required. The row check is an intersection on `data`, not a bound on `D` : with a
  // bound, one failing key made TS drop every key `data` supplied.
  draft<D extends object>(
    data: D & WriteRowOf<WriteShape> & DraftStrict<WriteShape, D>,
  ): DraftResult<DraftShape, D> {
    return applyProto(data, this.query as ProtoQuery) as DraftResult<DraftShape, D>
  }

  // draft() for a row inside the join tree, addressed by join aliases (`["rooms", "beds"]`). A column the join fills
  // in is optional. Returns one row whatever the join's cardinality.
  draftNested<P extends JoinPaths<Q>, D extends object>(
    path: P,
    data: D &
      RowAtPath<WriteRowOf<WriteShape>, P> &
      DraftStrict<RowAtPath<WriteRowOf<WriteShape>, P>, D>,
  ): DraftResult<RowAtPath<WriteRowOf<DraftShape>, P>, D> {
    let node = this.query as ProtoQuery
    for (const alias of path as readonly string[]) {
      const next = node.join?.[alias]
      if (next === undefined) {
        throw new Error(
          `draftNested: no join "${alias}" in path [${(path as readonly string[]).join(", ")}]`,
        )
      }
      node = next
    }
    return applyProto(data, node) as DraftResult<RowAtPath<WriteRowOf<DraftShape>, P>, D>
  }

  // Returns `obj` itself where nothing was substituted.
  private substituteParams(obj: unknown, params: { [name: string]: unknown }): unknown {
    if (obj == null) {
      return obj
    }
    if (Array.isArray(obj)) {
      if (obj[0] === "$param") {
        // FIXME maybe do some checking if a type was supplied in obj[2]
        const key = obj[1]
        if (typeof key !== "string") {
          throw new Error("find a better error name") // do that claude
        }
        return params[key]
      }
      let diff = false
      const res = new Array(obj.length)
      for (let i = 0, l = obj.length; i < l; i++) {
        res[i] = this.substituteParams(obj[i], params)
        diff = diff || res[i] !== obj[i]
      }
      return diff ? res : obj
    }
    if (typeof obj === "object") {
      let diff = false
      const res: { [name: string]: unknown } = {}
      const _obj = obj as { [name: string]: unknown }
      for (const x of Object.getOwnPropertyNames(obj)) {
        const orig = _obj[x]
        const r = this.substituteParams(orig, params)
        res[x] = r
        diff = diff || r !== orig
      }
      return diff ? res : obj
    }
    return obj
  }

  private doQuery(params: Params): Query {
    if (!this.hasParam) {
      return this.wireQuery as Query
    }
    return this.substituteParams(
      this.wireQuery,
      (params ?? {}) as { [name: string]: unknown },
    ) as Query
  }

  get(...args: OptionalWhenNoParams<Params>): Promise<Shape> {
    return this._send(this.doQuery(args[0] as Params))
  }

  // `data` is the write shape, which differs from the read shape where a query uses get/set/col.
  write(_params: Params, data: WriteShape): Promise<Shape>
  write(data: WriteShape): Promise<Shape>
  write(_params: Params | WriteShape, _data?: WriteShape): Promise<Shape> {
    const params = _data != null ? (_params as Params) : (void 0 as Params)
    const data = _data != null ? _data : (_params as WriteShape)
    return this._send({ query: this.doQuery(params), data })
  }

  private async _send(query: unknown) {
    const { codec } = this
    const res = await fetch(`${this.baseUrl.replace(/\/+$/, "")}/rel`, {
      credentials: "include",
      method: "POST",
      body: codec.encode(query, { structures: this.sendStructures }) as BodyInit,
      headers: codec.contentType
        ? { "Content-Type": codec.contentType, Accept: codec.accept ?? codec.contentType }
        : undefined,
    })
    if (res.status >= 400) {
      throw new RelRequestError(res.status, await res.text())
    }
    const data = codec.decode(
      codec.binary ? new Uint8Array(await res.arrayBuffer()) : await res.text(),
    )
    return applyProto(data, this.query as ProtoQuery) as Shape
  }
}

// "/" (this origin) unless changed : see the `baseUrl` declaration above.
Querier.prototype.baseUrl = "/"

// On unless changed : see the `sendStructures` declaration above.
Querier.prototype.sendStructures = true

// The browser's JSON unless changed : see the `codec` declaration above.
Querier.prototype.codec = {
  encode: (value) => JSON.stringify(value),
  decode: (body) => JSON.parse(body as string),
}

/**
 Thrown by get()/write() on a >= 400 status. `body` is the raw response text, unparsed.
 */
export class RelRequestError extends Error {
  constructor(
    public status: number,
    public body: string,
  ) {
    super(`rel request failed with status ${status}: ${body}`)
  }
}
