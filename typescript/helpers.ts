import { type Query, type RelationQuery } from "./query"

// biome-ignore lint/suspicious/noEmptyInterface: will be overriden
export interface Reations {}

// biome-ignore lint/suspicious/noEmptyInterface: will be overriden
export interface Functions {}

// biome-ignore lint/suspicious/noEmptyInterface: will be overriden
export interface Wellknowns {
  //
}

// Holds human-readable, LSP-completable relation-link identifiers for developer reuse ; not part of the query language itself.
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

// `shortcut` is interpreted client-side by relation()/function() to fill in `on:` and is never sent to the server.
// `on:` alone can't tell TypeScript whether the embed is an object or an array ; `unique` does.
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

// Resolves ONE join entry's row shape the same way relation()'s `rel: R` string does for the root, from its own
// inline schema/relation fields. Falls back to the permissive default when schema is omitted (no search_path
// available here) or the name is unrecognized.
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

// A join entry's own nested `join`, so joins-of-joins keep resolving recursively like the root query does.
type ExtractJoinMap<Q> = Q extends { join: infer J extends { [name: string]: unknown } } ? J : {}

// Known gap : every join below types as `T[]` regardless of real cardinality, since RelationQuery's JSON carries
// no FK-direction info. `Relationships` (still a stub above) is the natural place to fix this once it exists.
type JoinShapes<Join extends { [name: string]: unknown }, Depth extends number> = {
  [A in keyof Join]: ShapeFromRelationQuery<Join[A], ResolveJoinModel<Join[A]>, Digits[Depth]>[]
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

// A known relation name resolves to its real column shape ; anything else falls back to the permissive default.
// Deliberately ONE generic signature, not two overloads : with two, a bad column on a known relation would
// silently fall through to the looser unchecked overload instead of erroring (TS only flags "no overload
// matches" when every candidate fails).
type ResolveRelationModel<R extends string> = R extends keyof Relations
  ? Relations[R]
  : { [name: string]: unknown }

// Builder for relations. `rel` must be a fully qualified "schema.relation" name.
export function relation<R extends string, const Q extends RelationQuery<ResolveRelationModel<R>>>(
  rel: R,
  request: Q,
): Querier<ShapeFromQuery<Q, ResolveRelationModel<R>>, Params<Q>> {
  const [schema, relation] = rel.split(".")
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

// Both wellknown() and relation() produce a Querier with its Shape/Params known through their own return types.
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

  // Explicit Promise<Shape> return type : _send() only returns Promise<any> (res.json() can't know what it
  // parsed), so without this annotation Shape would be silently erased. write()'s overloads do the same.
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
