import { type Query, type RelationQuery } from "./query"

export interface Relations {
  // Will be augmented by rel
}

export interface Functions {
  // Will be augmented by rel
}

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

export interface Relationships {
  "hotel.rooms": 
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

// This type extract the exact shape of the query, going to its "select" and introspecting it to know what shape the result shall be
// Multiple subtypes might have to be created for it to function well
export type ShapeFromQuery<Q extends RelationQuery> = null

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
): Querier<unknown, Params<Q>> {
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
  get(_params: Params) {
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
