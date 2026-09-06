/*!
Section 3 of `specs/typescript.md ## File layout` : `RelationQuery`'s supporting type-level machinery — turning
a query's own `select`/`join`/`relation`/`function`/`shortcut` fields into the JSON shape it produces, and
extracting whatever `$param`s it declares. No runtime code lives here ; everything below is erased at compile time.
*/
import type { RelationQuery } from "./query"
import type {
  Functions,
  FunctionsByName,
  Relations,
  Relationships,
  Wellknowns,
} from "./schema.example"

export type RelationName = keyof Relations
export type FunctionName = keyof Functions
export type WellknownName = keyof Wellknowns

// Types for $param that shall be enforced
type TypeMap = {
  string: string
  number: number
  boolean: boolean
  date: Date
}

// Depth budget for ParamUnion's walk below : Query/Relation nest arbitrarily deep, which blows past TS's
// "excessively deep" instantiation heuristic without an explicit floor. Raise it if a real query needs more.
type Digits = [never, 0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11]

// Walk the tree, emit a union of single-key objects.
type ParamUnion<T, Depth extends number = 12> = Depth extends 0
  ? never
  : T extends readonly ["$param", infer K extends string, infer V extends keyof TypeMap]
    ? { [P in K]: TypeMap[V] }
    : T extends readonly unknown[]
      ? ParamUnion<T[number], Digits[Depth]>
      : T extends object
        ? ParamUnion<T[keyof T], Digits[Depth]>
        : never

type UnionToIntersection<U> = (U extends unknown ? (x: U) => void : never) extends (
  x: infer I,
) => void
  ? I
  : never

type Flatten<T> = { [K in keyof T]: T[K] }

// Walks a query to extract whatever params there were inside
export type Params<Q> = Flatten<UnionToIntersection<ParamUnion<Q>>>

///////////////////////////////////////////////////////////////////////
// ShapeFromQuery : infers the JSON result shape of a RelationQuery from
// its own `select`/`join`, against Rel (the concrete row type). Rel is threaded in explicitly rather than
// extracted from Q, since a literal Q no longer carries it once narrowed ; only the call site (relation()'s
// R -> ResolveRelationModel<R>, below) still knows it.

export type DefaultRow = { [name: string]: unknown }

// Resolves ANY query node's row shape — root or nested join alike — from whichever of `shortcut` (join sugar),
// `relation`, or `function` it carries. One resolver for every node means root and join resolution can't drift
// apart the way two hand-maintained types could. Falls back to the permissive default when schema is omitted (no
// search_path available here), the name is unrecognized, or neither `shortcut`/`relation`/`function` is given (a
// real query would fail server-side either way).
export type ResolveModel<Node> = Node extends { shortcut: infer S extends string }
  ? Extract<Relationships[keyof Relationships], { shortcut: S }> extends {
      relation: infer R extends object
    }
    ? R
    : DefaultRow
  : Node extends { schema: infer Sc extends string; relation: infer R extends string }
    ? `${Sc}.${R}` extends keyof Relations
      ? Relations[`${Sc}.${R}`]
      : DefaultRow
    : Node extends { relation: infer R extends string }
      ? R extends keyof Relations
        ? Relations[R]
        : DefaultRow
      : Node extends { schema: infer Sc extends string; function: infer F extends string }
        ? `${Sc}.${F}` extends keyof Functions
          ? ResolveFunctionModel<`${Sc}.${F}`>
          : DefaultRow
        : Node extends { function: infer F extends string }
          ? F extends keyof Functions
            ? ResolveFunctionModel<F>
            : DefaultRow
          : DefaultRow

// A function's embeddable row shape : its own `relation` (set-returning, joinable/selectable like a table) when
// given, else its scalar `returns` type — a scalar function produces one bare value per row, not a row at all.
// Functions[F] can itself be a union : Postgres allows several functions to share one name, distinguished only
// by argument list (overloading), and the generated schema files one Functions[key] entry per overload, unioned
// (tsgen's own renderFunctions). DistributeOverload exists solely so its own M is a NAKED type parameter at the
// point of the extends-check below — TS only distributes a conditional over a union when the checked type is a
// bare type parameter of that same conditional, and `Functions[F]` (an indexed access) isn't one ; boxing it
// through DistributeOverload<Functions[F]> restores that, so a mix of set-returning and scalar overloads
// resolves each member on its own (a per-overload union of the "right" branch) instead of every member being
// forced through whichever single branch the WHOLE union happens to satisfy (typically `returns`, unwrapped
// arrays and all — the wrong shape for func()'s select/join machinery, which wants one row, not `Rel[]`).
type ResolveFunctionModel<F extends keyof Functions> = DistributeOverload<Functions[F]>

type DistributeOverload<M> = M extends { relation: infer R extends object }
  ? R
  : M extends { returns: infer Ret }
    ? Ret
    : DefaultRow

// A join entry's own nested `join`, so joins-of-joins keep resolving recursively like the root query does.
// `keyof {} = never` is exactly what the "no join" case needs, since JoinShapes maps over this via `keyof` —
// Record<keyof any, never>/{[name: string]: unknown} (biome's own suggested replacements) both have `keyof` =
// string|number(|symbol) instead, which would map to a bogus index signature on FullShape for every query that
// joins nothing at all.
// biome-ignore lint/complexity/noBannedTypes: see above — `{}` is deliberate here, not a placeholder
type ExtractJoinMap<Q> = Q extends { join: infer J extends { [name: string]: unknown } } ? J : {}

// Cardinality comes from the Relationships variant matching `shortcut`'s own literal value (found by scanning
// every variant across every key, not just one — `shortcut` alone already uniquely identifies the FK) when a
// join used the `shortcut` sugar ; a join written out by hand (no `shortcut`) has no cardinality source, so it
// defaults to an array as before.
type JoinCardinality<J> = J extends { shortcut: infer S extends string }
  ? Extract<Relationships[keyof Relationships], { shortcut: S }> extends {
      unique: infer U extends boolean
    }
    ? U
    : false
  : false

type JoinShapes<Join extends { [name: string]: unknown }, Depth extends number> = {
  [A in keyof Join]: JoinCardinality<Join[A]> extends true
    ? ShapeFromRelationQuery<Join[A], ResolveModel<Join[A]>, Digits[Depth]>
    : ShapeFromRelationQuery<Join[A], ResolveModel<Join[A]>, Digits[Depth]>[]
}

type OwnShape<Rel extends object> = { [K in keyof Rel]: Rel[K] }

type FullShape<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = OwnShape<Rel> & JoinShapes<Join, Depth>

// A nominal marker meaning "this Expression's key doesn't exist on this side" — `get` on the write side, `set`
// on the read side (query.ts's `get`/`get-set`/`set` doc comments ; specs/query-engine.md ## Writability : "get
// doesn't count toward this at all"). A branded object rather than `never`, so a column that's legitimately typed
// `never` isn't also silently dropped by the same key-remapping mechanism.
declare const OmittedTag: unique symbol
type Omitted = { [OmittedTag]: true }

// ShapeFromExpression is split into one small type per GROUP of related Expression tags, rather than one long
// chain checking all ~20 tags in sequence ; the dispatcher at the bottom just picks which group applies.

type OwnFullTag =
  | "own"
  | "full"
  | "own_except"
  | "full_except"
  | "own_and"
  | "full_and"
  | "own_except_and"
  | "full_except_and"

type ContainerTag = "arr" | "array" | "lst" | "list" | "coalesce"

// Primitives and bare identifier references — every Expression form that's neither a tuple nor an inline-object.
// Falls back to `unknown` for a bare string that isn't a known column/join alias, as a safe default over `never`.
type ShapeFromLeaf<
  E,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = E extends null
  ? null
  : E extends true
    ? true
    : E extends false
      ? false
      : E extends number
        ? E
        : E extends "*"
          ? FullShape<Rel, Join, Depth>
          : E extends keyof Rel
            ? Rel[E]
            : E extends keyof Join
              ? JoinShapes<Join, Depth>[E]
              : unknown

// own/full and their except/and variants : the same own-vs-full × plain/except/and/except_and grid,
// spelled out one tag at a time rather than computed generically.
type ShapeFromOwnFullTag<
  Tag extends OwnFullTag,
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Tag extends "own"
  ? OwnShape<Rel>
  : Tag extends "full"
    ? FullShape<Rel, Join, Depth>
    : Tag extends "own_except"
      ? Rest extends readonly [infer Ex extends readonly string[]]
        ? Omit<Rel, Ex[number]>
        : never
      : Tag extends "full_except"
        ? Rest extends readonly [infer Ex extends readonly string[]]
          ? Omit<FullShape<Rel, Join, Depth>, Ex[number]>
          : never
        : Tag extends "own_and"
          ? Rest extends readonly [infer And extends { [name: string]: unknown }]
            ? OwnShape<Rel> & ShapeFromExpressionMap<And, Rel, Join, Depth>
            : never
          : Tag extends "full_and"
            ? Rest extends readonly [infer And extends { [name: string]: unknown }]
              ? FullShape<Rel, Join, Depth> & ShapeFromExpressionMap<And, Rel, Join, Depth>
              : never
            : Tag extends "own_except_and"
              ? Rest extends readonly [
                  infer Ex extends readonly string[],
                  infer And extends { [name: string]: unknown },
                ]
                ? Omit<Rel, Ex[number]> & ShapeFromExpressionMap<And, Rel, Join, Depth>
                : never
              : // "full_except_and", the only tag left once every branch above is excluded
                Rest extends readonly [
                    infer Ex extends readonly string[],
                    infer And extends { [name: string]: unknown },
                  ]
                ? Omit<FullShape<Rel, Join, Depth>, Ex[number]> &
                    ShapeFromExpressionMap<And, Rel, Join, Depth>
                : never

// get/get-set : the referenced column's own type, on the read side (get-set's default_set only matters on
// write). `set` never appears in a read response at all (query.ts's own doc comment : "this column is not
// fetched in query mode") — its key is dropped from the containing object entirely, not merely typed oddly ; see
// `Omitted`, above, and ShapeFromExpressionMap's key remap, below.
type ShapeFromFieldTag<
  Tag extends "get" | "get-set" | "set",
  Rest extends readonly unknown[],
  Rel extends object,
> = Tag extends "set"
  ? Omitted
  : Rest extends readonly [infer Col extends string, ...unknown[]]
    ? Col extends keyof Rel
      ? Rel[Col]
      : unknown
    : unknown

// $param : resolved through the same TypeMap ParamUnion (above) uses, so a declared cast infers the real value
// type. No cast defaults to ::jsonb server-side — genuinely unknown here.
type ShapeFromParamTag<Rest extends readonly unknown[]> = Rest extends readonly [
  string,
  infer V extends keyof TypeMap,
]
  ? TypeMap[V]
  : unknown

// ["call", identifier, ...arguments] : a function call, most commonly a computed column (query-engine.md ##
// Reading Algorithm : "a function taking the relation's row type as its argument, callable via alias.func_name
// or func_name(alias)"). A bare (unqualified) identifier resolves through FunctionsByName — tsgen's own
// search_path-disambiguated index, since that unqualified form is the entire point of Postgres' `t.func_name()`
// computed-column sugar — an explicit `{schema,name}` resolves through Functions directly. Neither goes through
// ResolveFunctionModel's `relation`-unwrapping (shapes.ts, above) : that exists for func()'s embed-as-a-table
// path, not for a single value selected inline here — a set-returning function's own `returns` (its real,
// array-shaped JSON value) is what a "call" tag actually produces, not its unwrapped per-row type. A dynamic
// (non-literal) or unrecognized identifier falls back to `unknown`, the same limitation relation()/func()
// already have for a non-literal "schema.relation" name.
type ShapeFromCallTag<Rest extends readonly unknown[]> = Rest extends readonly [
  infer Id,
  ...unknown[],
]
  ? Id extends string
    ? Id extends keyof FunctionsByName
      ? ReturnsOf<FunctionsByName[Id]>
      : unknown
    : Id extends { schema: infer Sc extends string; name: infer N extends string }
      ? `${Sc}.${N}` extends keyof Functions
        ? ReturnsOf<Functions[`${Sc}.${N}`]>
        : unknown
      : unknown
  : unknown

// Every Functions[key] entry always has `returns` (unlike `relation`, present only conditionally) — a
// uniformly-matching union already infers the union of each member's own `returns` without needing
// DistributeOverload's boxing trick above (that trick only matters when SOME members fail to match a branch,
// which can't happen here : `returns` is unconditional).
type ReturnsOf<M> = M extends { returns: infer Ret } ? Ret : unknown

// arr/array/lst/list preserve each item's position (a tuple, not a collapsed union) ; coalesce instead
// produces one value — the union of what each argument could be.
type ShapeFromContainerTag<
  Tag extends ContainerTag,
  Items extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Tag extends "coalesce"
  ? ShapeFromExpression<Items[number], Rel, Join, Depth>
  : { [I in keyof Items]: ShapeFromExpression<Items[I], Rel, Join, Depth> }

// Key remapping (`as`) drops any entry whose value resolved to `Omitted` (a `set` tag, on the read side) instead
// of keeping the key with a nonsensical type — the key genuinely isn't present in the response.
type ShapeFromExpressionMap<
  Obj extends { [name: string]: unknown },
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = {
  [K in keyof Obj as ShapeFromExpression<Obj[K], Rel, Join, Depth> extends Omitted
    ? never
    : K]: ShapeFromExpression<Obj[K], Rel, Join, Depth>
}

// The JSON value one Expression node produces : routes to whichever group above matches E's own tag. Order
// matters : `["own"]`/`["full"]` and their variants must be checked before the fallback [string] literal case,
// or a length-1 array like `["own"]` matches [string] first — see query.ts's own `Expression` doc comment.
//
// "call" resolves through ShapeFromCallTag (above), a literal-identifier lookup against FunctionsByName/
// Functions. Raw operators, "agg", "index"/"slice", "format", ... still fall back to `unknown` : narrowing them
// needs a JSON-schema export of the database (not currently implemented — see database.json's
// status) — separate, not-yet-built (v2) work.
type ShapeFromExpression<
  E,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number = 12,
> = Depth extends 0
  ? unknown
  : E extends readonly [infer Tag extends string, ...infer Rest extends readonly unknown[]]
    ? Tag extends OwnFullTag
      ? ShapeFromOwnFullTag<Tag, Rest, Rel, Join, Digits[Depth]>
      : Tag extends "get" | "get-set" | "set"
        ? ShapeFromFieldTag<Tag, Rest, Rel>
        : Tag extends "$param"
          ? ShapeFromParamTag<Rest>
          : Tag extends ContainerTag
            ? ShapeFromContainerTag<Tag, Rest, Rel, Join, Digits[Depth]>
            : Tag extends "call"
              ? ShapeFromCallTag<Rest>
              : Rest extends readonly [] // the plain [string] literal form ; Tag wasn't a reserved keyword above
                ? Tag
                : unknown // any other operator/agg/index/slice/format tuple — see comment above
    : E extends { [name: string]: unknown }
      ? ShapeFromExpressionMap<E, Rel, Join, Digits[Depth]>
      : ShapeFromLeaf<E, Rel, Join, Digits[Depth]>

// The JSON result shape of one RelationQuery node : its own `select` (absent defaults to `["full"]`, per
// query.ts's `select` doc comment), evaluated against Rel and its own `join` (resolved recursively above).
//
// A bare top-level `set` (e.g. `select: ["set", "col"]`, not inside a map) has nowhere to drop its own key — the
// whole node's shape WOULD be `Omitted` itself. Falls back to `unknown` rather than leaking that internal marker ;
// what a query that fetches nothing at all actually returns isn't modeled here.
export type ShapeFromRelationQuery<
  Q,
  Rel extends object,
  Depth extends number = 12,
> = Depth extends 0
  ? unknown
  : Q extends { select: infer Sel }
    ? [Sel] extends [undefined]
      ? FullShape<Rel, ExtractJoinMap<Q>, Digits[Depth]>
      : ShapeFromExpression<Sel, Rel, ExtractJoinMap<Q>, Digits[Depth]> extends infer S
        ? S extends Omitted
          ? unknown
          : S
        : never
    : FullShape<Rel, ExtractJoinMap<Q>, Digits[Depth]>

// Public entry point. Rel defaults to RelationQuery's own permissive default so `ShapeFromQuery<Q>` alone still
// typechecks without going through relation()'s R -> Rel resolution (which passes the real Rel explicitly).
//
// Read shape only — see `WriteShapeFromQuery`, below, for the `data` shape `write()` actually needs.
export type ShapeFromQuery<
  Q extends RelationQuery<Rel>,
  Rel extends object = { [name: string]: unknown },
> = ShapeFromRelationQuery<Q, Rel>

///////////////////////////////////////////////////////////////////////
// WriteShapeFromQuery : the `data` shape a write actually expects, mirroring ShapeFromQuery above field-for-field
// except at the `get`/`get-set`/`set` leaf, where read and write genuinely diverge (query.ts's own doc comments ;
// specs/query-engine.md ## Writability). `get` is dropped (read-only) ; `set`/`get-set`/a bare column reference
// are kept, matching "a physical column is writable iff referenced ... wrapped only by coalescing operators, or
// by set/get-set ; get doesn't count toward this at all."
//
// NOT modeled, and left entirely to the server : the exactly-once occurrence rule, `insert_columns`/
// `update_columns` allowlisting, identity-target writability, and write_mode-dependent required/optional columns
// — specs/typescript.md ## Goals already calls out full expression/column type-checking as a separate, harder
// (v2) concern, and this is the write-side instance of that same gap. This type is a best-effort narrowing, not
// a validator ; the server remains the actual authority on what's writable.

type WriteJoinShapes<Join extends { [name: string]: unknown }, Depth extends number> = {
  [A in keyof Join]: JoinCardinality<Join[A]> extends true
    ? WriteShapeFromRelationQuery<Join[A], ResolveModel<Join[A]>, Digits[Depth]>
    : WriteShapeFromRelationQuery<Join[A], ResolveModel<Join[A]>, Digits[Depth]>[]
}

type WriteFullShape<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = OwnShape<Rel> & WriteJoinShapes<Join, Depth>

// get is dropped (read-only) ; get-set/set are writable, same underlying column type as the read side.
type WriteShapeFromFieldTag<
  Tag extends "get" | "get-set" | "set",
  Rest extends readonly unknown[],
  Rel extends object,
> = Tag extends "get"
  ? Omitted
  : Rest extends readonly [infer Col extends string, ...unknown[]]
    ? Col extends keyof Rel
      ? Rel[Col]
      : unknown
    : unknown

type WriteShapeFromOwnFullTag<
  Tag extends OwnFullTag,
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Tag extends "own"
  ? OwnShape<Rel>
  : Tag extends "full"
    ? WriteFullShape<Rel, Join, Depth>
    : Tag extends "own_except"
      ? Rest extends readonly [infer Ex extends readonly string[]]
        ? Omit<Rel, Ex[number]>
        : never
      : Tag extends "full_except"
        ? Rest extends readonly [infer Ex extends readonly string[]]
          ? Omit<WriteFullShape<Rel, Join, Depth>, Ex[number]>
          : never
        : Tag extends "own_and"
          ? Rest extends readonly [infer And extends { [name: string]: unknown }]
            ? OwnShape<Rel> & WriteShapeFromExpressionMap<And, Rel, Join, Depth>
            : never
          : Tag extends "full_and"
            ? Rest extends readonly [infer And extends { [name: string]: unknown }]
              ? WriteFullShape<Rel, Join, Depth> &
                  WriteShapeFromExpressionMap<And, Rel, Join, Depth>
              : never
            : Tag extends "own_except_and"
              ? Rest extends readonly [
                  infer Ex extends readonly string[],
                  infer And extends { [name: string]: unknown },
                ]
                ? Omit<Rel, Ex[number]> & WriteShapeFromExpressionMap<And, Rel, Join, Depth>
                : never
              : // "full_except_and", the only tag left once every branch above is excluded
                Rest extends readonly [
                    infer Ex extends readonly string[],
                    infer And extends { [name: string]: unknown },
                  ]
                ? Omit<WriteFullShape<Rel, Join, Depth>, Ex[number]> &
                    WriteShapeFromExpressionMap<And, Rel, Join, Depth>
                : never

type WriteShapeFromContainerTag<
  Tag extends ContainerTag,
  Items extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Tag extends "coalesce"
  ? WriteShapeFromExpression<Items[number], Rel, Join, Depth>
  : { [I in keyof Items]: WriteShapeFromExpression<Items[I], Rel, Join, Depth> }

// Same key-remap as ShapeFromExpressionMap, dropping `Omitted` entries — here that's a `get` tag instead of `set`.
type WriteShapeFromExpressionMap<
  Obj extends { [name: string]: unknown },
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = {
  [K in keyof Obj as WriteShapeFromExpression<Obj[K], Rel, Join, Depth> extends Omitted
    ? never
    : K]: WriteShapeFromExpression<Obj[K], Rel, Join, Depth>
}

type WriteShapeFromExpression<
  E,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number = 12,
> = Depth extends 0
  ? unknown
  : E extends readonly [infer Tag extends string, ...infer Rest extends readonly unknown[]]
    ? Tag extends OwnFullTag
      ? WriteShapeFromOwnFullTag<Tag, Rest, Rel, Join, Digits[Depth]>
      : Tag extends "get" | "get-set" | "set"
        ? WriteShapeFromFieldTag<Tag, Rest, Rel>
        : Tag extends "$param"
          ? ShapeFromParamTag<Rest>
          : Tag extends ContainerTag
            ? WriteShapeFromContainerTag<Tag, Rest, Rel, Join, Digits[Depth]>
            : Tag extends "call"
              ? Omitted // never a real column (query-engine.md : "never a candidate for writability") ; dropped
              : // from the write shape entirely, same as `get` above, rather than kept with a nonsensical type.
                Rest extends readonly []
                ? Tag
                : unknown
    : E extends { [name: string]: unknown }
      ? WriteShapeFromExpressionMap<E, Rel, Join, Digits[Depth]>
      : ShapeFromLeaf<E, Rel, Join, Digits[Depth]>

// A bare top-level `get` has nowhere to drop its own key, same edge case ShapeFromRelationQuery guards against
// for a bare top-level `set` — falls back to `unknown` rather than leaking `Omitted`.
export type WriteShapeFromRelationQuery<
  Q,
  Rel extends object,
  Depth extends number = 12,
> = Depth extends 0
  ? unknown
  : Q extends { select: infer Sel }
    ? [Sel] extends [undefined]
      ? WriteFullShape<Rel, ExtractJoinMap<Q>, Digits[Depth]>
      : WriteShapeFromExpression<Sel, Rel, ExtractJoinMap<Q>, Digits[Depth]> extends infer S
        ? S extends Omitted
          ? unknown
          : S
        : never
    : WriteFullShape<Rel, ExtractJoinMap<Q>, Digits[Depth]>

// Public entry point, mirroring ShapeFromQuery.
export type WriteShapeFromQuery<
  Q extends RelationQuery<Rel>,
  Rel extends object = { [name: string]: unknown },
> = WriteShapeFromRelationQuery<Q, Rel>
