/*!
Section 1 of `specs/typescript.md ## File layout` : the actual code — `Querier`, `relation()`, `func()`, `join()`,
`wellknown()`. This is what a developer calls directly ; it comes first in the generated file precisely so it's
the first thing seen when opening it, ahead of the developer's own schema (section 2) and the type machinery that
powers both (section 3, `query.ts`/`shapes.ts`).
*/
import type { Query, RelationQuery } from "./query"
import type { Functions, Relationships, Wellknowns } from "./schema.example"
import type {
  DefaultRow,
  DeferredFunctionArgs,
  FunctionArgs,
  FunctionName,
  MatchOverload,
  Params,
  Prettify,
  RelationName,
  RequiredKeysOf,
  ResolveModel,
  RootShapeFromFunctionMember,
  ShapeFromQuery,
  WithProto,
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

// relation()'s R is constrained to RelationName, so an unrecognized name is a compile error at the call site
// rather than reaching this type. Delegates to ResolveModel so root and join resolution share one path.
type ResolveRelationModel<R extends string> = ResolveModel<{ relation: R }>

// `join()` scoped to relation K, so a nested join never has to repeat the enclosing relation's name — see
// scopedJoin() below and specs/typescript-better-join.md. K isn't constrained to keyof Relationships here (unlike
// relation()'s own R) because it's also fed a shortcut's parsed TargetRelationName, a plain template-literal
// string ; SafeRelationships resolves to `never` for an unrecognized one, making the scoped join uncallable there
// rather than a type error at the declaration site.
type SafeRelationships<K extends string> = K extends keyof Relationships ? Relationships[K] : never

// The target relation name embedded in a shortcut string itself : "hotel.rooms<;id:property_id" ->
// "hotel.rooms". This is what lets a nested join's own callback re-scope itself to ITS target without a second
// explicit argument — the shortcut the caller already had to type carries it.
type TargetRelationName<S extends string> = S extends `${infer Rel}${"<" | ">"}${string}`
  ? Rel
  : never

// Three call signatures, not one signature with a union parameter type : `proto`'s `ThisType` (specs/typescript-
// proto.md ## `proto` field, shapes.ts's WithProto doc comment) only resolves `this` correctly when the object
// literal is checked against a single, non-union parameter type — folded into a union with the other forms, it
// silently stops applying. The callback form's own return type is `WithProto<Q, Rel>`, not bare `Q`, for the same
// reason : a `proto` inside the object the callback returns needs that same isolated contextual type.
export type ScopedJoin<K extends string> = {
  <
    S extends SafeRelationships<K>["shortcut"],
    const Q extends RelationQuery<Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
  >(
    shortcut: S,
    request?: WithProto<Q, Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
  ): Q & { shortcut: S }
  <
    S extends SafeRelationships<K>["shortcut"],
    const Q extends RelationQuery<Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
  >(
    shortcut: S,
    request: Querier<unknown, unknown, unknown, Q>,
  ): Q & { shortcut: S }
  <
    S extends SafeRelationships<K>["shortcut"],
    const Q extends RelationQuery<Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
  >(
    shortcut: S,
    request: (
      join: ScopedJoin<TargetRelationName<S>>,
    ) => WithProto<Q, Extract<SafeRelationships<K>, { shortcut: S }>["relation"]>,
  ): Q & { shortcut: S }
}

// Builds the `(shortcut, request?) => ...` closure handed to a relation()/join() callback, scoped to `key` —
// shared by relation() (scoped to the relation itself) and join()'s own recursive case (scoped to the shortcut's
// target, once parsed).
function scopedJoin<K extends string>(key: K): ScopedJoin<K> {
  return ((shortcut: string, request?: unknown) =>
    join(key as never, shortcut as never, request as never)) as ScopedJoin<K>
}

// Resolves `request` the same way relation() and join() each need to : a callback is invoked with a join scoped
// to `key` ; an absent request defaults to a bare `{}` (select-all, per query-language/selecting.md's own
// "absent select defaults to full" rule) ; a plain object is used as-is.
function resolveRequest<K extends string, Q>(
  key: K,
  request: Q | ((join: ScopedJoin<K>) => Q) | undefined,
): Q {
  if (typeof request === "function") {
    return (request as (join: ScopedJoin<K>) => Q)(scopedJoin(key))
  }
  return request ?? ({} as Q)
}

// A relation root is never a scalar function call, so — unlike func() below — it's unconditionally array-wrapped :
// shapes.ts's RootShapeFromFunctionMember doc comment (## Root cardinality) covers the full reasoning ;
// writing.md : "an array of rows at the root". RequiredKeysOf<R> is passed explicitly : R (e.g. "hotel.properties")
// is split apart from `request`/Q, so Q alone never carries the `relation`/`schema` fields WriteShapeFromQuery's
// own default would otherwise read a relation's required columns off of (see that type's own doc comment,
// shapes.ts). Same root-array wrap as the read side — writing.md : "data" is "an array of rows at the root".
type RelationQuerier<
  R extends RelationName,
  Q extends RelationQuery<ResolveRelationModel<R>>,
> = Querier<
  ShapeFromQuery<Q, ResolveRelationModel<R>>[],
  WriteShapeFromQuery<Q, ResolveRelationModel<R>, RequiredKeysOf<R>>[],
  Params<Q>,
  Q
>

// Builder for relations. `rel` must be a fully qualified "schema.relation" name. `request` is optional (a bare
// select-all query, per resolveRequest above) and can be a plain query object or a callback receiving a `join`
// already scoped to `rel`, so a nested join never has to repeat the relation it's being joined from.
//
// Two overloads, not one signature with a union parameter type : see ScopedJoin's own doc comment, above — a
// `proto`'s `ThisType` only resolves `this` correctly against a single, non-union parameter type.
export function relation<
  R extends RelationName,
  const Q extends RelationQuery<ResolveRelationModel<R>>,
>(rel: R, request?: WithProto<Q, ResolveRelationModel<R>>): RelationQuerier<R, Q>
export function relation<
  R extends RelationName,
  const Q extends RelationQuery<ResolveRelationModel<R>>,
>(
  rel: R,
  request: (join: ScopedJoin<R>) => WithProto<Q, ResolveRelationModel<R>>,
): RelationQuerier<R, Q>
export function relation<
  R extends RelationName,
  const Q extends RelationQuery<ResolveRelationModel<R>>,
>(
  rel: R,
  request?: WithProto<Q, ResolveRelationModel<R>> | ((join: ScopedJoin<R>) => Q),
): RelationQuerier<R, Q> {
  const [schema, relation] = rel.split(".")
  const query = {
    ...resolveRequest(rel, request),
    schema,
    relation,
  } as Q
  return new Querier(query)
}

// func()'s F is constrained to FunctionName, so an unrecognized name is a compile error at the call site rather
// than reaching this type. RelationQuery's Rel must extend object — meaningful for a set-returning function
// (select/join operate on its `relation`) but not for a scalar one (`returns: number`, say), which falls back to
// the permissive default instead. A scalar function is better called via an Expression's `["call", ...]` tag than
// through func()'s select/join machinery anyway.
type ResolveCalledFunctionModel<F extends string> =
  ResolveModel<{ function: F }> extends infer M extends object ? M : DefaultRow

// Builder for functions. `fn` must be a fully qualified "schema.function" name. `function` can't be used as an
// identifier (reserved word), hence `func`. `request.arguments`, if given, is checked against the function's own
// declared `positional_args`/`args` (shapes.ts's DeferredFunctionArgs) rather than RelationQuery's own generic,
// unchecked default — see shapes.ts's own doc comment on FunctionArgs/DeferredFunctionArgs.
export function func<
  F extends FunctionName,
  const Q extends RelationQuery<
    ResolveCalledFunctionModel<F>,
    { [name: string]: RelationQuery },
    DeferredFunctionArgs<Functions[F]>
  >,
>(
  fn: F,
  request: WithProto<Q, ResolveCalledFunctionModel<F>>,
): Querier<
  // Root cardinality (shapes.ts's RootShapeFromFunctionMember doc comment, ## Root cardinality) : a
  // set-returning overload's row shape wrapped in an array, same as relation() ; a scalar overload's bare
  // `returns` value instead — that's the ONE case (server/rel.go's streamItem) a root query response isn't an
  // array. F, not Q, drives this : same reasoning as relation()'s explicit RequiredKeysOf<R> above — Q alone
  // never carries the `function`/`schema` fields.
  RootShapeFromFunctionMember<F extends keyof Functions ? Functions[F] : never, Q>,
  WriteShapeFromQuery<Q, ResolveCalledFunctionModel<F>>,
  Params<Q>,
  Q
> {
  const [schema, fn_name] = fn.split(".")
  const query = {
    ...request,
    schema,
    function: fn_name,
  } as Q
  return new Querier(query)
}

// Direct call to `fn`, with no select/join/where step : sends `args` and resolves to the function's own return
// value (a scalar for a scalar overload, an array of rows for a set-returning one — same root cardinality as
// func(), see shapes.ts's RootShapeFromFunctionMember), rather than a Querier to further shape or defer. `args`
// must match one of `fn`'s declared `positional_args`/`args` shapes exactly (shapes.ts's FunctionArgs — plain
// values only, no `$param`) ; which overload it matches (shapes.ts's MatchOverload) drives the return type, so an
// overloaded function like "hotel.property_average_rating" resolves to the specific overload actually called,
// not a union of every overload's return type.
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
// this join's row shape and cardinality from it alone, without needing `key` again. `request` is optional and can
// be a callback, same as relation() — see ScopedJoin/scopedJoin above and specs/typescript-better-join.md ; the
// callback it receives is scoped to `shortcut`'s own TARGET (parsed via TargetRelationName), not `key`, so a
// join-of-a-join never repeats a relation name either.
// docs/content/typescript/index.md ## Building a query : `request` also accepts a Querier built by a standalone
// relation()/func() call, so a `select`/`proto` already written once can be joined in as-is instead of being
// retyped. `on`/`schema`/`relation`/`shortcut` always come from `shortcut` regardless of which form `request`
// takes — a Querier built through relation() never carries `on`, since a root query isn't itself a join.
// Three overloads, not one signature with a union parameter type : see ScopedJoin's own doc comment, above — a
// `proto`'s `ThisType` only resolves `this` correctly against a single, non-union parameter type, whether it's
// this node's own `proto` (first overload) or one nested inside the callback form's own returned object (third
// overload, hence its `WithProto`-typed return rather than a bare `Q`). The Querier-reuse form (second overload)
// doesn't involve a `proto` object literal at all, so it's safe to keep on its own without needing `WithProto`.
export function join<
  K extends keyof Relationships,
  S extends Relationships[K]["shortcut"],
  const Q extends RelationQuery<Extract<Relationships[K], { shortcut: S }>["relation"]>,
>(
  key: K,
  shortcut: S,
  request?: WithProto<Q, Extract<Relationships[K], { shortcut: S }>["relation"]>,
): Q & { shortcut: S }
export function join<
  K extends keyof Relationships,
  S extends Relationships[K]["shortcut"],
  const Q extends RelationQuery<Extract<Relationships[K], { shortcut: S }>["relation"]>,
>(key: K, shortcut: S, request: Querier<unknown, unknown, unknown, Q>): Q & { shortcut: S }
export function join<
  K extends keyof Relationships,
  S extends Relationships[K]["shortcut"],
  const Q extends RelationQuery<Extract<Relationships[K], { shortcut: S }>["relation"]>,
>(
  key: K,
  shortcut: S,
  request: (
    join: ScopedJoin<TargetRelationName<S>>,
  ) => WithProto<Q, Extract<Relationships[K], { shortcut: S }>["relation"]>,
): Q & { shortcut: S }
export function join<
  K extends keyof Relationships,
  S extends Relationships[K]["shortcut"],
  const Q extends RelationQuery<Extract<Relationships[K], { shortcut: S }>["relation"]>,
>(
  _key: K,
  shortcut: S,
  request?:
    | WithProto<Q, Extract<Relationships[K], { shortcut: S }>["relation"]>
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

// docs/content/typescript/index.md ## Attaching behavior to rows : walks `query`'s own `relation`/`join` tree
// alongside the response value it produced, applying that node's `proto` (if any) to every row it reaches.
// Cardinality is read off the response value itself (array vs. object vs. null), never off any type-level
// source — JoinCardinality (shapes.ts) is erased at compile time and has no runtime counterpart.
type ProtoQuery = {
  proto?: object
  join?: { [name: string]: ProtoQuery }
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
    Object.setPrototypeOf(value, query.proto)
  }
  return value
}

// Both wellknown() and relation() produce a Querier with its Shape/WriteShape/Params known through their own
// return types. WriteShape defaults to Shape so a hand-built Querier (or wellknown(), which doesn't compute one)
// still works ; relation()/func() always supply the real, narrower WriteShapeFromQuery explicitly. Q defaults to
// the widened `Query` for the same reason ; relation()/func() supply their own literal Q instead, so join() can
// recover it from a reused Querier — docs/content/typescript/index.md ## Building a query.
export class Querier<Shape = unknown, WriteShape = Shape, Params = void, Q = Query> {
  public query: Q

  constructor(
    query: Q,
    public has_params = false,
  ) {
    this.query = Querier.stripShortcut(query) as Q
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
    })
      .then((res) => res.json())
      .then((data) => applyProto(data, this.query as ProtoQuery) as Shape)
  }
}

// Named types to hold onto a Querier's own Shape/WriteShape, e.g. as a function parameter's type, without
// re-deriving it via `Awaited<ReturnType<typeof someQuery.get>>` every time. `Prettify` (shapes.ts) is applied
// here too, on top of it already being applied at every level `Shape`/`WriteShape` were built from (shapes.ts's
// ShapeFromRelationQuery/WriteShapeFromRelationQuery doc comments) — a hand-built `Querier` (unlike one
// relation()/func() produced) never went through either, so these still prettify a raw `Shape`/`WriteShape` for
// one. Array-wrapped (the common case, root or to-many join) prettifies the element, not the array itself, same
// guard as `Prettify` (shapes.ts) has to have anyway.
export type ShapeOf<Q extends Querier<unknown, unknown, unknown, unknown>> =
  Q extends Querier<infer S, unknown, unknown, unknown>
    ? S extends readonly (infer E)[]
      ? Prettify<E>[]
      : Prettify<S>
    : never

export type WriteShapeOf<Q extends Querier<unknown, unknown, unknown, unknown>> =
  Q extends Querier<unknown, infer W, unknown, unknown>
    ? W extends readonly (infer E)[]
      ? Prettify<E>[]
      : Prettify<W>
    : never
