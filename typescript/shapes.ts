/*!
Section 3 of `specs/typescript.md ## File layout` : `RelationQuery`'s supporting type-level machinery — turning
a query's own `select`/`join`/`relation`/`function`/`shortcut` fields into the JSON shape it produces, and
extracting whatever `$param`s it declares. No runtime code lives here ; everything below is erased at compile time.
*/
import type { RelationQuery } from "./query"
import type { Functions, Relations, Relationships, Wellknowns } from "./schema.example"

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
type ResolveFunctionModel<F extends keyof Functions> = Functions[F] extends {
  relation: infer R extends object
}
  ? R
  : Functions[F] extends { returns: infer Ret }
    ? Ret
    : DefaultRow

// A join entry's own nested `join`, so joins-of-joins keep resolving recursively like the root query does.
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

// get/get-set : the referenced column's own type. Both produce the same read shape (get-set's default_set only matters on write).
type ShapeFromFieldTag<
  Rest extends readonly unknown[],
  Rel extends object,
> = Rest extends readonly [infer Col extends string, ...unknown[]]
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

type ShapeFromExpressionMap<
  Obj extends { [name: string]: unknown },
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = { [K in keyof Obj]: ShapeFromExpression<Obj[K], Rel, Join, Depth> }

// The JSON value one Expression node produces : routes to whichever group above matches E's own tag. Order
// matters : `["own"]`/`["full"]` and their variants must be checked before the fallback [string] literal case,
// or a length-1 array like `["own"]` matches [string] first — see query.ts's own `Expression` doc comment.
//
// Raw operators, "call"/"agg", "index"/"slice", "format", ... fall back to `unknown` : narrowing them needs the
// database.json export specs/typescript.md's "Goals" already calls out as separate, not-yet-built (v2) work.
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
      : Tag extends "get" | "get-set"
        ? ShapeFromFieldTag<Rest, Rel>
        : Tag extends "$param"
          ? ShapeFromParamTag<Rest>
          : Tag extends ContainerTag
            ? ShapeFromContainerTag<Tag, Rest, Rel, Join, Digits[Depth]>
            : Rest extends readonly [] // the plain [string] literal form ; Tag wasn't a reserved keyword above
              ? Tag
              : unknown // any other operator/call/agg/index/slice/format tuple — see comment above
    : E extends { [name: string]: unknown }
      ? ShapeFromExpressionMap<E, Rel, Join, Digits[Depth]>
      : ShapeFromLeaf<E, Rel, Join, Digits[Depth]>

// The JSON result shape of one RelationQuery node : its own `select` (absent defaults to `["full"]`, per
// query.ts's `select` doc comment), evaluated against Rel and its own `join` (resolved recursively above).
export type ShapeFromRelationQuery<
  Q,
  Rel extends object,
  Depth extends number = 12,
> = Depth extends 0
  ? unknown
  : Q extends { select: infer Sel }
    ? [Sel] extends [undefined]
      ? FullShape<Rel, ExtractJoinMap<Q>, Digits[Depth]>
      : ShapeFromExpression<Sel, Rel, ExtractJoinMap<Q>, Digits[Depth]>
    : FullShape<Rel, ExtractJoinMap<Q>, Digits[Depth]>

// Public entry point. Rel defaults to RelationQuery's own permissive default so `ShapeFromQuery<Q>` alone still
// typechecks without going through relation()'s R -> Rel resolution (which passes the real Rel explicitly).
//
// Read shape only : get/set's read-vs-write asymmetry (query.ts's `Expression` doc comments) means the `data`
// shape for a write isn't always identical to a read's ; `write()` below still types `data` as this read Shape,
// exact only when get/set are both absent.
export type ShapeFromQuery<
  Q extends RelationQuery<Rel>,
  Rel extends object = { [name: string]: unknown },
> = ShapeFromRelationQuery<Q, Rel>
