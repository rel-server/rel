import { type Query, type RelationQuery } from "./query"

// biome-ignore lint/suspicious/noEmptyInterface: will be overriden
export interface Reations {}

// biome-ignore lint/suspicious/noEmptyInterface: will be overriden
export interface Functions {}

// biome-ignore lint/suspicious/noEmptyInterface: will be overriden
export interface Wellknowns {
  //
}

// The purpose of this object is to hold human-readable, LSP completable identifiers that describe a link to another relation that the developer can then reuse. This is clearly a helper that is not part of the query language - which can still be used
export interface Relationships {}

export type RelationName = keyof Relations
export type FunctionName = keyof Functions
export type WellknownName = keyof Wellknowns

// Examples of generation

export type RoomStatus = "clean" | "dirty"

interface Table__Hotel__Rooms {
  /* same goes for columns */
  property_id: number // not null references hotel.properties (id),
  room_type_id: number // not null references hotel.room_types (id),
  room_number: string
  floor: number | null
  status: RoomStatus // default 'clean', //: should we handle default creation client-side ?
  features: string[] // default '{}'
}

interface Table__Hotel__Properties {
  /** And comments for each column are added */
  id: number
  chain_id: number | null
  name: string
  location: Point //: this type does not really have a direct equivalent, probably that it will have to be created in some pg_types.ts file
  star_rating: number | null
  description: string | null
  created_at: Date | null //: do we do Temporal directly or do we keep Date ? It could be interesting to have it as a requirement ? (or at least a polyfill)
}

export interface Relations {
  "hotel.rooms": Table__Hotel__Rooms
  "hotel.properties": Table__Hotel__Properties
}

export interface FunctionRelations {
  "hotel.rooms_available": Table__Hotel__Properties
}

// rel will inform on the relationships by adding a field on RelationQuery that is not to be sent to the server but instead interpreted by relation() and function() to fill the on: property while deleting the `shortcut`. This is purely for developer convenience.
// `on:` alone would not allow typescript to guess if the embedded resource is a single object or an array ; the unique field accomplishes just that.
export interface Relationships {
  "hotel.rooms": {
    shortcut: "hotel.properties;id:property_id"
    unique: true
    relation: Table__Hotel__Properties
  }
  "hotel.properties": {
    incoming: "hotel.rooms;id:property_id"
    unique: false
    relation: Table__Hotel__Rooms
  }
}

// For each function that is exported, we get an export
export interface Functions {
  "hotel.property_average_rating": {
    positional_args: [Table__Hotel__Properties]
    args: { property: Table__Hotel__Properties }
    returns: number
  }
  "hotel.rooms_available": {
    positional_args: [property_id: number, on_date?: Date]
    args: { property_id: number; on_date?: Date }
    relation: Table__Hotel__Properties // this function has an underlying
    returns: Table__Hotel__Properties[]
  }
}

///////////////////////////////////////////////////////////////////////
// Type helpers
//

// Types for $param that shall be enforced
type TypeMap = {
  string: string
  number: number
  boolean: boolean
  date: Date
}

// Depth budget for ParamUnion's walk below : Query/Relation are
// self-referential (join nests Relation inside Relation, Expression nests
// arbitrarily), so nothing about the walk's own shape proves termination to
// the compiler — without an explicit floor, distributing the conditional
// over every property/union member at every level blows past TS's
// "excessively deep" instantiation heuristic on any realistically nested
// query, even though the walk always terminates in practice (a real query
// is finite). 12 is comfortably past any hand-written query's real nesting
// depth ; raise it if a legitimate query ever needs deeper params picked up.
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
// its own `select`/`join`, against Rel (the concrete row type). Rel is a
// separate parameter, not extracted from Q itself : once Q has been
// narrowed down to a literal (`const Q extends RelationQuery<Rel>`), its
// own inferred type no longer carries Rel around anywhere inside it —
// only the call site that combined a relation name with its resolved
// column type (relation()'s R -> ResolveRelationModel<R>, below) still
// knows it, so it has to be threaded in explicitly.

// Resolves ONE join entry's row shape from its own literal schema/relation
// fields, the same way relation()'s `rel: R` string does for the root — a
// join has no fully-qualified string of its own, only inline
// schema/relation fields in the JSON tree. When schema is omitted (search
// path resolution, same as the root) there's no way to know which schema
// was actually meant without the server's own configured search_path,
// which isn't available here ; falls back to the permissive default in
// that case, same as an unrecognized relation name does everywhere else
// in this file.
type ResolveJoinModel<J> = J extends {
  schema: infer S extends string
  relation: infer R extends string
}
  ? `${S}.${R}` extends keyof Relations
    ? Relations[`${S}.${R}`]
    : { [name: string]: unknown }
  : J extends { relation: infer R extends string }
    ? R extends keyof Relations
      ? Relations[R]
      : { [name: string]: unknown }
    : { [name: string]: unknown } // function-rooted join, or neither given (a real query would fail server-side) — nothing to resolve statically

// A join entry's own nested `join`, if any, so joins-of-joins keep
// resolving recursively the same way the root query does.
type ExtractJoinMap<Q> = Q extends { join: infer J extends { [name: string]: unknown } } ? J : {}

// Cardinality caveat : every join below is typed as an array, since
// RelationQuery's own JSON carries no foreign-key-direction information —
// a to-one "belongs to" embed is actually a bare object, not an array,
// per query.ts's own `join` doc comment, but that direction lives in the
// database schema, not the query, and isn't modeled anywhere in this file
// yet. `Relationships` (still a stub above) is the natural place for it
// once it exists ; until then, every join reads as `T[]` here regardless
// of its real cardinality — a known, not-yet-fixable gap, not an
// oversight.
type JoinShapes<Join extends { [name: string]: unknown }, Depth extends number> = {
  [A in keyof Join]: ShapeFromRelationQuery<Join[A], ResolveJoinModel<Join[A]>, Digits[Depth]>[]
}

type OwnShape<Rel extends object> = { [K in keyof Rel]: Rel[K] }

type FullShape<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = OwnShape<Rel> & JoinShapes<Join, Depth>

// ShapeFromExpression is split into one small type per GROUP of related
// Expression tags below, rather than one long chain checking all ~20 tags
// in sequence — each group is short enough to read on its own, and the
// dispatcher at the bottom (the actual `ShapeFromExpression`) only has to
// pick which group applies, not re-derive each tag's own logic inline.
// Every group takes the same trailing (Rel, Join, Depth) parameters, so
// they compose the same way regardless of which one a given Expression
// routes through.

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

// Primitives and bare identifier references — every Expression form that's
// neither a tuple nor an inline-object. Falls back to `unknown` for a bare
// string that isn't actually a known column/join alias, which shouldn't
// happen for an E that already passed RelationQuery's own
// Keys<Rel>|Keys<Join> constraint check, but is a safe default rather than
// `never` if it ever does.
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

// own/full and their except/and variants : each produces an object shape —
// the enclosing relation's own columns (`own`) or that plus every join's
// own embed (`full`), optionally minus an `except` column list and/or plus
// computed `and` columns. Grouped together since they're the same
// own-vs-full × plain-vs-except-vs-and-vs-except_and grid, just spelled
// out one tag at a time rather than computed generically.
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

// get/get-set : the referenced column's own type. Both tags produce the
// same read shape (get-set's own default_set only matters on write), so
// there's nothing to distinguish between them here.
type ShapeFromFieldTag<
  Rest extends readonly unknown[],
  Rel extends object,
> = Rest extends readonly [infer Col extends string, ...unknown[]]
  ? Col extends keyof Rel
    ? Rel[Col]
    : unknown
  : unknown

// $param : resolved through the same TypeMap ParamUnion (above) already
// uses, so a param with a declared cast infers as the real value type.
// No cast given defaults to ::jsonb server-side — genuinely unknown here.
type ShapeFromParamTag<Rest extends readonly unknown[]> = Rest extends readonly [
  string,
  infer V extends keyof TypeMap,
]
  ? TypeMap[V]
  : unknown

// arr/array/lst/list preserve each item's own position (a proper tuple,
// not a collapsed union array) ; coalesce instead produces ONE value — the
// union of what each argument could be, since only one of them is ever
// the actual runtime result.
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

// The JSON value one Expression node produces : routes to whichever group
// above matches E's own tag, given the enclosing relation's own row type
// (bare column references) and its joins (join-alias references, own/
// full's own embedding). Order matters for tuples : `["own"]`/`["full"]`
// and their variants are checked BEFORE the fallback single-string-literal
// case, since a length-1 array like `["own"]` would otherwise structurally
// match `[string]` first and be read as the literal string "own" instead
// of the own/full tag — see query.ts's own `Expression` doc comment on
// exactly this ambiguity.
//
// Several tags (raw operators, "call"/"agg", "index"/"slice", "format",
// ...) fall back to `unknown` rather than a precise type : their real
// result type depends on Postgres function/operator signatures this file
// has no introspected knowledge of — narrowing them needs the
// database.json export specs/typescript.md's own "Goals" section already
// calls out as separate, not-yet-built work (flagged there as likely v2).
// `unknown` is the honest answer today, not a wrong one.
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

// The JSON result shape of one RelationQuery node : its own `select` (or,
// absent, the same as an explicit `["full"]`, per query.ts's own `select`
// doc comment — "If not specified... equivalent to select * from the
// relation, as well as the embeds defined by join"), evaluated against
// Rel (this node's own row type) and its own `join` (its children,
// resolved recursively via ExtractJoinMap/ResolveJoinModel above).
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

// Public entry point. Rel defaults to RelationQuery's own permissive
// default so `ShapeFromQuery<Q>` alone still typechecks for a Q built
// without going through relation()'s R -> Rel resolution ; relation()
// itself always passes the real, resolved Rel explicitly (see below).
//
// Read shape only : `get`'s "not looked for / modified in write mode" and
// `set`'s "not fetched in query mode, expected in write mode" asymmetry
// (query.ts's own `Expression` doc comments) means the shape POSTed as
// `data` for a write isn't always identical to what a read returns — a
// separate WriteShapeFromQuery would be needed to type that side
// precisely. Not attempted here ; `write()` below still types `data` as
// the same read Shape, which is right for the common case (get/set both
// absent) but not exact once either is used.
export type ShapeFromQuery<
  Q extends RelationQuery<Rel>,
  Rel extends object = { [name: string]: unknown },
> = ShapeFromRelationQuery<Q, Rel>

///////////////////////////////////////////////////////////////////////
// Actual code
//

// Build a Querier for this wellknown
// This function will be overloaded with as many declare as there are well-known queries
export function wellknown(
  wellknown: string,
  params: { [name: string]: unknown },
): Querier<unknown, Params<typeof params>> {
  return new Querier({
    wellknown,
    params,
  })
}

// A known relation name resolves to its real column shape (real
// completion/checking on select/where/...) ; anything else (a dynamic
// string not in Relations) falls back to the permissive default. This is
// ONE generic signature, deliberately not two overloads : with two
// overloads, a known relation name whose request fails the specific,
// column-checked overload silently falls through to a second, unchecked
// "general case" overload instead of erroring — TS only reports "no
// overload matches" when EVERY candidate fails, so the looser fallback
// quietly absorbs the mistake with no diagnostic at all. A single
// conditionally-resolved signature has no second candidate to fall back
// into, so a bad column on a known relation name is always caught.
type ResolveRelationModel<R extends string> = R extends keyof Relations
  ? Relations[R]
  : { [name: string]: unknown }

// specialized builder for relations. rel *must* be a fully qualified name
// the given request is pretty much the shape of a regular request, except it is compatible with
// at its simplest, will just produce something that is pretty much just
//
export function relation<R extends string, const Q extends RelationQuery<ResolveRelationModel<R>>>(
  rel: R,
  request: Q,
): Querier<ShapeFromQuery<Q, ResolveRelationModel<R>>, Params<Q>> {
  const [schema, relation] = rel.split(".")
  //
  const query = {
    ...request,
    schema,
    relation,
  }
  return new Querier(query)
}

const res = relation("hotel.properties", {
  select: {
    col: "created_at",
    test: ["$param", "toto", "string"],
  },
}).get({ toto: "sdfkj" })

// both wellknown and relation end up creating queries, that _may_ be locally parametrized in js-land
// Shape will be known thanks to the overloads to wellknown() and relation()
// as for Params
export class Querier<Shape = unknown, Params = void> {
  constructor(
    public query: Query,
    public has_params = false,
  ) {}

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

  private doQuery(params: Params) {
    return null
  }

  // fetch the request
  //
  // Explicit Promise<Shape> return type : _send()'s own return type is
  // Promise<any> (res.json() has no way to know what it parsed), and
  // without an annotation here get()'s inferred return type would just be
  // that Promise<any> verbatim — silently erasing Shape (and, by
  // extension, everything ShapeFromQuery computes) the moment a caller
  // looked at the result. write()'s own overloads already avoid this the
  // same way, by declaring Promise<Shape> explicitly rather than letting
  // it infer from the body.
  get(_params: Params): Promise<Shape> {
    return this._send(this.doQuery(_params))
  }

  // send a write request to rel
  write(_params: Params, data: Shape): Promise<Shape>
  write(data: Shape): Promise<Shape>
  write(_params: Params | Shape, _data?: Shape): Promise<Shape> {
    const params = _data != null ? (_params as Params) : (void 0 as Params)
    const data = _data != null ? _data : (_params as Shape)
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
