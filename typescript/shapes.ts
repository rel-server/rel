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
  RequiredColumns,
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

// Forces an intersection/mapped-type chain to display as one flat object on hover, instead of the chain of
// aliases that produced it. Guarded against arrays and non-object types (a scalar, or an already-array-wrapped
// root/join shape reaching here) : mapping over an array's own keys (numeric indices, `length`, methods) would
// produce nonsense, and `keyof` on a non-object type doesn't exist at all.
//
// Also guarded against a branded primitive (docs/content/typescript/index.md ## Typed wire values' `string & { readonly __pg: ... }`
// types) : confirmed a real bug, not just a theoretical one — `string & { brand }` DOES satisfy `T extends
// object` (an intersection with an object type), so without this guard `{[K in keyof T]: T[K]} & {}` maps over
// every `String.prototype` member too, producing a method-bag object no longer assignable back to the original
// branded string at all. Checking the primitive union first, before `extends object`, is what avoids this.
export type Prettify<T> = T extends readonly unknown[]
  ? T
  : T extends string | number | boolean | bigint | symbol
    ? T
    : T extends object
      ? { [K in keyof T]: T[K] } & {}
      : T

// Walks a query to extract whatever params there were inside
export type Params<Q> = Prettify<UnionToIntersection<ParamUnion<Q>>>

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

// The literal "schema.table" key ResolveModel resolved a real relation's row shape from — needed to look up
// RequiredColumns[key] (specs/required-fields.md) for write-shape required-ness, below. A join's `shortcut`
// already spells this out as its own leading segment (querier.ts's own TargetRelationName does the identical
// extraction for its recursive join scoping) ; `never` for a function-rooted node (a function's row was never
// introspected as an insertable relation — functions.md : "always read-only") or an unrecognized name, the same
// cases ResolveModel itself falls back to DefaultRow for.
export type ResolveKey<Node> = Node extends { shortcut: infer S extends string }
  ? S extends `${infer Key}${"<" | ">"}${string}`
    ? Key
    : never
  : Node extends { schema: infer Sc extends string; relation: infer R extends string }
    ? `${Sc}.${R}`
    : Node extends { relation: infer R extends string }
      ? R
      : never

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

///////////////////////////////////////////////////////////////////////
// Function call arguments — func()/call() (querier.ts) share this rather than the generic, unchecked
// Expression<Keys<Rel>>[]/{...} RelationQuery's own `arguments` field defaults to : a function's `positional_args`/
// `args` already fully describe the real argument shape (tuple or named object, optional trailing args and
// composite/row-shaped args included), so there's nothing left for a looser Expression-based type to check for.
// Boxed the same way DistributeOverload is (M stays a naked type parameter), so an overloaded function's two
// argument shapes are offered as a union rather than collapsing onto one.

// call()'s own argument type : plain values only, exactly as `positional_args`/`args` declare them — no
// `$param`, no identifier/sub-expression forms. call() executes immediately (no deferred Querier, no `.get()`
// params step), so there is nothing to substitute later.
export type FunctionArgs<M> = M extends {
  positional_args: infer P extends readonly unknown[]
  args: infer A extends object
}
  ? P | A
  : never

// func()'s own argument type : same shape as FunctionArgs, but each argument slot also accepts a `["$param",
// name, cast?]` placeholder (query.ts's own Expression tag) — func() returns a deferred Querier, reused across
// calls with different `.get(params)`/`.write(params, data)` values, the same way its `where`/`select` already do.
export type DeferredFunctionArgs<M> = M extends {
  positional_args: infer P extends readonly unknown[]
  args: infer A extends object
}
  ? MappedWithParam<P> | MappedWithParam<A>
  : never

type ParamPlaceholder = readonly ["$param", string, string?]

type MappedWithParam<T> = { [K in keyof T]: T[K] | ParamPlaceholder }

// The overload(s) of an overloaded function whose OWN `positional_args`/`args` accepts the caller's actual
// `Args` — call()'s return type is resolved through this rather than through DistributeOverload/
// ResolveFunctionModel directly, so e.g. hotel.property_average_rating's two overloads (one scalar, one
// set-returning) each still resolve to their own return type based on which one the caller's arguments match,
// rather than collapsing onto a union of both regardless of which was actually called. Boxed the same way
// DistributeOverload is (M stays a naked type parameter).
export type MatchOverload<M, Args> = M extends {
  positional_args: infer P
  args: infer A
}
  ? Args extends P | A
    ? M
    : never
  : never

///////////////////////////////////////////////////////////////////////
// Root cardinality (docs/content/query-language/writing.md : "an array of rows at the root and at any incoming
// join ..., a single object at an outgoing join" ; server/rel.go's streamItem doc comment : "a bare scalar for a
// scalar function root, a JSON array otherwise") — the ROOT of a query is never itself a to-one join, so it's
// always array-shaped UNLESS it's a scalar function call (no `relation`, `compileFunctionCall`'s own path,
// entirely bypassing select/join). JoinShapes/WriteJoinShapes (above) already get this right for every NESTED
// node via JoinCardinality ; this section is the root-only counterpart querier.ts's relation()/func() and a
// well-known query's own generated `shape`/`write_shape` all need on top of it.

// One function overload's own root contribution : set-returning (`relation` present) wraps its row shape in an
// array, same as any other relation root ; scalar (`returns` only, no `relation`) is the one exception — its
// bare `returns` value IS the response, select/join never applies to it. Boxed the same way DistributeOverload
// is (M stays a naked type parameter) so a MIXED overload set (e.g. hotel.property_average_rating) resolves
// each member through its own branch and unions them, rather than collapsing onto whichever branch every member
// happens to satisfy.
//
// The `[M] extends [never]` guard : an unrecognized function name (relation()/func()'s R/F, or
// RootShapeFromLiteralQuery below, all fall back to `never` for that case) must still resolve to the safe
// DefaultRow[] default — a bare `M extends {...}` here would instead DISTRIBUTE over `never` (M is a naked type
// parameter), collapsing the whole conditional to `never` itself rather than reaching either branch.
export type RootShapeFromFunctionMember<M, Q, Depth extends number = 12> = [M] extends [never]
  ? DefaultRow[]
  : RootShapeFromFunctionMemberEach<M, Q, Depth>

type RootShapeFromFunctionMemberEach<M, Q, Depth extends number> = M extends {
  relation: infer R extends object
}
  ? ShapeFromRelationQuery<Q, R, Depth>[]
  : M extends { returns: infer Ret }
    ? Prettify<Ret>
    : DefaultRow[]

// Root-level wrap for a query node that still carries its own `relation`/`function` fields on its own literal —
// a well-known query's embedded `as const` literal (schema.example.ts), unlike relation()/func()'s own Q (which
// never carries them ; see querier.ts's relation()/func(), which apply the equivalent wrap themselves, driven by
// the relation/function NAME rather than by inspecting Q). A relation root is unconditionally array-wrapped ; a
// function root defers to RootShapeFromFunctionMember above.
export type RootShapeFromLiteralQuery<
  Q extends { [name: string]: unknown },
  Depth extends number = 12,
> = Q extends { schema: infer Sc extends string; function: infer F extends string }
  ? RootShapeFromFunctionMember<
      `${Sc}.${F}` extends keyof Functions ? Functions[`${Sc}.${F}`] : never,
      Q,
      Depth
    >
  : Q extends { function: infer F extends string }
    ? RootShapeFromFunctionMember<F extends keyof Functions ? Functions[F] : never, Q, Depth>
    : ShapeFromRelationQuery<Q, ResolveModel<Q>, Depth>[]

// A join entry's own nested `join`, so joins-of-joins keep resolving recursively like the root query does.
// `keyof {} = never` is exactly what the "no join" case needs, since JoinShapes maps over this via `keyof` —
// Record<keyof any, never>/{[name: string]: unknown} (biome's own suggested replacements) both have `keyof` =
// string|number(|symbol) instead, which would map to a bogus index signature on FullShape for every query that
// joins nothing at all.
// biome-ignore lint/complexity/noBannedTypes: see above — `{}` is deliberate here, not a placeholder
type ExtractJoinMap<Q> = Q extends { join: infer J extends { [name: string]: unknown } } ? J : {}

// Cardinality comes straight off `shortcut`'s own direction marker (`>`/`<`/`*` — docs/content/
// typescript/index.md ## Building a query), pattern-matched on its template literal type, not a
// separate `unique` field lookup : `*` is to-many, `<`/`>` are both to-one. A join written out by
// hand (no `shortcut`) has no cardinality source, so it defaults to an array as before.
type JoinCardinality<J> = J extends { shortcut: infer S extends string }
  ? S extends `${string}*${string}`
    ? false
    : true
  : false

// One joined member's own shape — to-one unwrapped, to-many array-wrapped per JoinCardinality. Shared by JoinShapes
// (every member, mapped) and ShapeFromDotTag (below, one member looked up by name via a bare "." hop).
type JoinMemberShape<J, Depth extends number> =
  JoinCardinality<J> extends true
    ? ShapeFromRelationQuery<J, ResolveModel<J>, Depth>
    : ShapeFromRelationQuery<J, ResolveModel<J>, Depth>[]

type JoinShapes<Join extends { [name: string]: unknown }, Depth extends number> = {
  [A in keyof Join]: JoinMemberShape<Join[A], Digits[Depth]>
}

type OwnShape<Rel extends object> = { [K in keyof Rel]: Rel[K] }

type FullShape<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = OwnShape<Rel> & JoinShapes<Join, Depth>

// A nominal marker meaning "this Expression's key doesn't exist on this side" — `get` on the write side, `set`
// on the read side (query.ts's `get`/`col`/`set` doc comments ; specs/query-engine.md ## Writability : "get
// doesn't count toward this at all"). A branded object rather than `never`, so a column that's legitimately typed
// `never` isn't also silently dropped by the same key-remapping mechanism.
declare const OmittedTag: unique symbol
type Omitted = { [OmittedTag]: true }

// ShapeFromExpression is split into one small type per GROUP of related Expression tags, rather than one long
// chain checking all ~20 tags in sequence ; the dispatcher at the bottom just picks which group applies.

type OwnFullTag = "*" | "*~"

type ContainerTag = "arr" | "array" | "lst" | "list" | "coalesce"

// Primitives and literal strings — every Expression form that's neither a tuple nor an inline-object. A bare
// string is always a literal now (never a column/alias reference — that needs ["col", name] or a "*"/"*~" tag),
// so this no longer needs Rel/Join at all.
type ShapeFromLeaf<E> = E extends null
  ? null
  : E extends true
    ? true
    : E extends false
      ? false
      : E extends number
        ? E
        : E extends string
          ? E
          : unknown

// "*"/"*~" (full/own) resolve to the same base shape either way ; the optional trailing [except?, and?] args are
// dispatched by shape (array vs object), not position, so either may be given alone or both together.
type OwnFullBase<
  Tag extends OwnFullTag,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Tag extends "*~" ? OwnShape<Rel> : FullShape<Rel, Join, Depth>

// Folds an arbitrary-length "*"/"*~" rest-tuple (each item either a K[] except-list or an
// and-map, in any order, any count) into one union of excepted keys / one merged and-map —
// every except-list concatenated, every and-map merged with later keys overriding earlier.
type ExceptFromRest<Rest extends readonly unknown[]> = Rest extends readonly [
  infer Head,
  ...infer Tail,
]
  ? Head extends readonly string[]
    ? Head[number] | ExceptFromRest<Tail>
    : ExceptFromRest<Tail>
  : never

type AndFromRest<Rest extends readonly unknown[]> = Rest extends readonly [
  infer Head,
  ...infer Tail,
]
  ? Head extends { [name: string]: unknown }
    ? Omit<Head, keyof AndFromRest<Tail>> & AndFromRest<Tail>
    : AndFromRest<Tail>
  : Record<string, never>

type ShapeFromOwnFullTag<
  Tag extends OwnFullTag,
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> =
  OwnFullBase<Tag, Rel, Join, Depth> extends infer Base extends object
    ? Omit<Base, ExceptFromRest<Rest>> & ShapeFromExpressionMap<AndFromRest<Rest>, Rel, Join, Depth>
    : never

// get/col : the referenced column's own type, on the read side (col's default_set only matters on write). `set`
// never appears in a read response at all (query.ts's own doc comment : "this column is not fetched in query
// mode") — its key is dropped from the containing object entirely, not merely typed oddly ; see `Omitted`,
// above, and ShapeFromExpressionMap's key remap, below.
type ShapeFromFieldTag<
  Tag extends "get" | "col" | "set",
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

// "."/"dot" : docs/content/query-language/selecting.md ## The dot-chain form — the generic "look this up in
// scope" building block a bare string used to be. A single hop resolves `name` against the current scope : a
// real column of Rel, a joined relation named in Join (same per-member shape JoinShapes gives it, to-one/to-many
// per JoinMemberShape above), or a computed field (docs/content/query-language/computed-fields.md) registered
// under that bare name via FunctionsByName — the same lookup ShapeFromCallTag already does for an explicit
// ["call", name], since a computed field is exactly a search-path function over the relation's own row type.
// FunctionsByName isn't itself scoped to Rel (it's a global, search_path-disambiguated index — tsgen's own doc
// comment on it), so a name collision with another relation's identically-named computed field could
// mistype here ; narrowing that further would need per-relation computed-field data this type doesn't have.
// A 2+-hop chain instead drills a base Expression's own resolved shape field by field, each hop a bare name
// (never re-parsed as a literal — query.ts's own Expression doc comment on "."/"dot").
type ShapeFromDotSingleHop<
  Name extends string,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Name extends keyof Rel
  ? Rel[Name]
  : Name extends keyof Join
    ? JoinMemberShape<Join[Name], Depth>
    : Name extends keyof FunctionsByName
      ? ReturnsOf<FunctionsByName[Name]>
      : unknown

type ShapeFromDotChain<Base, Hops extends readonly string[]> = Hops extends readonly [
  infer Hop extends string,
  ...infer Rest extends readonly string[],
]
  ? Base extends { [name: string]: unknown }
    ? Hop extends keyof Base
      ? ShapeFromDotChain<Base[Hop], Rest>
      : unknown
    : unknown
  : Base

type ShapeFromDotTag<
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Rest extends readonly [infer Name extends string]
  ? ShapeFromDotSingleHop<Name, Rel, Join, Depth>
  : Rest extends readonly [infer Base, ...infer Hops extends readonly string[]]
    ? ShapeFromDotChain<ShapeFromExpression<Base, Rel, Join, Depth>, Hops>
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

// The JSON value one Expression node produces : routes to whichever group above matches E's own tag.
//
// "call" resolves through ShapeFromCallTag (above), a literal-identifier lookup against FunctionsByName/
// Functions. Raw operators, "agg", "index"/"slice", "format", ... still fall back to `unknown` : narrowing them
// from THIS type-level position would need a type-level schema description, which GET /rel/database.json
// (specs/database-json.md) doesn't help with directly — it's a runtime JSON export a consumer resolves
// against at request time, not something the TypeScript compiler can consult while checking this file —
// separate, not-yet-built (v2) work.
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
      : Tag extends "get" | "col" | "set"
        ? ShapeFromFieldTag<Tag, Rest, Rel>
        : Tag extends "." | "dot"
          ? ShapeFromDotTag<Rest, Rel, Join, Digits[Depth]>
          : Tag extends "$param"
            ? ShapeFromParamTag<Rest>
            : Tag extends ContainerTag
              ? ShapeFromContainerTag<Tag, Rest, Rel, Join, Digits[Depth]>
              : Tag extends "call"
                ? ShapeFromCallTag<Rest>
                : unknown // any other operator/agg/index/slice/format tuple — see comment above
    : E extends { [name: string]: unknown }
      ? ShapeFromExpressionMap<E, Rel, Join, Digits[Depth]>
      : ShapeFromLeaf<E>

// The JSON result shape of one RelationQuery node, before `proto` (docs/content/typescript/index.md ## Attaching
// behavior to rows) merges in : its own `select` (absent defaults to `["*"]`, per query.ts's `select` doc
// comment), evaluated against Rel and its own `join` (resolved recursively above).
//
// A bare top-level `set` (e.g. `select: ["set", "col"]`, not inside a map) has nowhere to drop its own key — the
// whole node's shape WOULD be `Omitted` itself. Falls back to `unknown` rather than leaking that internal marker ;
// what a query that fetches nothing at all actually returns isn't modeled here.
type BaseShapeFromRelationQuery<Q, Rel extends object, Depth extends number> = Q extends {
  select: infer Sel
}
  ? [Sel] extends [undefined]
    ? FullShape<Rel, ExtractJoinMap<Q>, Digits[Depth]>
    : ShapeFromExpression<Sel, Rel, ExtractJoinMap<Q>, Digits[Depth]> extends infer S
      ? S extends Omitted
        ? unknown
        : S
      : never
  : FullShape<Rel, ExtractJoinMap<Q>, Digits[Depth]>

// docs/content/typescript/index.md ## Attaching behavior to rows : the row shape `proto`'s own getters/methods
// type `this` against — the node's shape before ITS OWN `proto` merges in, so a `proto` object never has to type
// its own members as part of its own input.
export type ProtoRowShape<
  Q,
  Rel extends object,
  Depth extends number = 12,
> = BaseShapeFromRelationQuery<Q, Rel, Depth>

// docs/content/typescript/index.md ## Attaching behavior to rows : reads one `proto` entry's effective
// member type, whether it's a plain method or a property descriptor (`{get, set}`/`{value}`).
// biome-ignore-start lint/suspicious/noExplicitAny: matching an arbitrary function/setter signature is load-bearing here
// `get`/`set`/`value` are matched as REQUIRED members here, not optional (`get?(): ...`) — a descriptor entry's
// own declared type must genuinely have (or omit) `set` for IsWritable to tell get-only from get+set apart ; see
// pg_values.ts's accessor helpers, which return a precise `{get(): T; set(v): void}`-shaped type for that reason,
// never `TypedPropertyDescriptor<T>` (whose get/set/value are ALL optional, making every entry structurally
// match both a required-set check and its negation, so it cannot tell writable apart from get-only at all).
type ExtractDescriptor<D> = D extends (...args: any[]) => any
  ? D
  : D extends { get(): infer T; set(v: any): void }
    ? T
    : D extends { get(): infer T }
      ? T
      : D extends { value: infer V }
        ? V
        : unknown

// Whether a `proto` entry is assignable — a plain method or a get-only descriptor is not.
type IsWritable<D> = D extends (...args: any[]) => any
  ? false
  : D extends { set(v: any): void }
    ? true
    : false
// biome-ignore-end lint/suspicious/noExplicitAny: matching an arbitrary function/setter signature is load-bearing here

// Turns a `proto` descriptor map into the plain member shape it produces once applied as a prototype. The
// writable half MUST use `-readonly`, not bare mapped-type syntax : a homomorphic mapped type over a
// `const`-inferred (deeply readonly) P inherits `readonly` per key regardless of IsWritable's own result.
//
// ForWrite makes the get-only half OPTIONAL rather than required — only WriteShapeFromRelationQuery sets it. A
// get-only member is never populated by the caller (assigning it is a compile error, same as the read shape) ;
// it's only ever present because `init()` (querier.ts) attaches it onto the row's prototype. Requiring it in the
// write shape would force a hand-built write literal (one NOT passed through `init()`) to fake a value for a
// column that doesn't exist server-side, for no benefit — the read shape keeps it required, since `applyProto`
// guarantees every fetched row genuinely has it.
type FromDescriptorMap<P, ForWrite extends boolean = false> = (ForWrite extends true
  ? {
      readonly [K in keyof P as IsWritable<P[K]> extends true ? never : K]?: ExtractDescriptor<P[K]>
    }
  : {
      readonly [K in keyof P as IsWritable<P[K]> extends true ? never : K]: ExtractDescriptor<P[K]>
    }) & {
  -readonly [K in keyof P as IsWritable<P[K]> extends true ? K : never]: ExtractDescriptor<P[K]>
}

// Merges `proto`'s own members into the row shape it decorates. ForWrite threads through to
// FromDescriptorMap — see that type's own doc comment.
type MergeProto<Q, Base, ForWrite extends boolean = false> = Q extends {
  proto: infer P extends object
}
  ? Base & FromDescriptorMap<P, ForWrite>
  : Base

// Prettify wraps every node's own output here, not just the root's : this is the one function root AND every
// nested join member resolve their row shape through (JoinShapes, above, and RootShapeFromFunctionMemberEach
// both call back into this same function per node), so wrapping it here makes every nesting level readable on
// hover for free, without a separate deep-recursive prettify walk of its own.
export type ShapeFromRelationQuery<
  Q,
  Rel extends object,
  Depth extends number = 12,
> = Depth extends 0
  ? unknown
  : Prettify<MergeProto<Q, BaseShapeFromRelationQuery<Q, Rel, Digits[Depth]>>>

// docs/content/typescript/index.md ## Attaching behavior to rows : the type relation()/func()/join() (querier.ts)
// put their own request literal's type through, intersected on top of `Q`, so `proto`'s own getters/methods have
// `this` typed against that exact node's `ProtoRowShape` — including, recursively, any nested join's own
// `proto`-merged shape (JoinShapes, above, already resolves every join member through `ShapeFromRelationQuery`).
//
// `proto`'s own type carries `ThisType<...>`, not a `base_class` parameter : a parameter's contextual type is
// resolved eagerly, during inference, before Q's other sibling fields (`join` in particular) are done inferring —
// a callback-shaped `proto` field breaks both its own `this` typing AND the rest of Q's inference in the process.
// `ThisType` instead resolves `this` lazily, once the object literal's members are already checked, matching how
// plain object methods/getters (no parameters) are the one part of an object literal that ISN'T
// context-sensitive. `NoInfer<Q>` keeps `proto` from being a second, conflicting inference site for Q — Q is
// inferred from the rest of the literal only.
//
// A `proto` mixing a `get` and a plain method (rather than only one kind) needs an explicit return type
// annotation on EVERY member, or TypeScript's own `this`-inference for the mix degrades silently to `any`
// throughout the object — confirmed empirically, not a documented TS behavior. Same-kind objects (all getters,
// or all plain methods) don't need the annotations.
export type WithProto<Q, Rel extends object> = Q & {
  proto?: object & ThisType<ProtoRowShape<NoInfer<Q>, Rel>>
}

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
// except at the `get`/`col`/`set` leaf, where read and write genuinely diverge (query.ts's own doc comments ;
// specs/query-engine.md ## Writability). `get` is dropped (read-only) ; `set`/`col` are kept — a bare column
// reference no longer exists (a bare string is a literal now), matching "a physical column is writable iff
// referenced ... wrapped only by coalescing operators, or by set/col ; get doesn't count toward this at all."
//
// A column named in RequiredColumns[key] (specs/required-fields.md — not nullable, no default, not
// identity/generated) stays mandatory ; every other physical column — nullable, defaulted, or simply one a
// hand-written select map chose not to include — is optional, matching what an actual INSERT can leave out.
// This applies per relation reached in the tree, root and every joined relation alike (WriteJoinShapes below
// recomputes it for each), and follows a column through a select map's own key rename (`{"renamed": "col"}"`) —
// BackingColumnOf resolves each entry's own VALUE back to the physical column it names, independent of whatever
// key it's filed under, so the association is never actually lost the way this spec's own `> Thoughts:` worried
// it might be.
//
// NOT modeled, and left entirely to the server : the exactly-once occurrence rule, `insert_columns`/
// `update_columns` allowlisting, identity-target writability, and write_mode-dependent required/optional columns
// (a column required for a fresh INSERT is still typed mandatory even when the actual write_mode in play is an
// update-only one, where Postgres wouldn't need it at all) — specs/typescript.md ## Goals already calls out full
// expression/column type-checking as a separate, harder (v2) concern, and this is the write-side instance of
// that same gap. This type is a best-effort narrowing, not a validator ; the server remains the actual authority
// on what's writable.

// RequiredColumns[key], or `never` when key isn't a real, recognized relation (ResolveKey's own fallback) —
// `never` indexed against RequiredColumns' own required `[schema.table]: ...` shape would otherwise be a type
// error, not a graceful empty union, hence the guard.
export type RequiredKeysOf<Key> = Key extends keyof RequiredColumns ? RequiredColumns[Key] : never

// The single physical column name E refers to, when that's unambiguous — a col/set field tag naming one (`get`
// is excluded : it's read-only, already dropped from the write shape via Omitted before this is ever
// consulted), or a single-hop "."/"dot" ([".", name] — exactly two elements, so a 2+-hop chain's own `base`
// never gets mistaken for a bare name here) naming a real column the same way (server/query/shape.go's own
// walkSelectForWritability records a plain `*Identifier` toward writability exactly like `col`/`set` — a
// single-hop `.` reference to a real column is just as "clean" server-side). A bare string is never a column
// reference any more (it's a literal), so it can't back one either. Anything else — "*"/"*~" and their variants,
// a computed/call expression, a multi-hop dot chain, a container, `$param` — has no ONE backing column, so it's
// `never` ; IsRequiredEntry (below) reads that as "can't be required," never as a false positive.
type BackingColumnOf<E, Rel extends object> = E extends readonly [
  "col" | "set",
  infer Col,
  ...unknown[],
]
  ? Col extends keyof Rel
    ? Col
    : never
  : E extends readonly ["." | "dot", infer Name extends string]
    ? Name extends keyof Rel
      ? Name
      : never
    : never

// Whether Obj[K]'s own expression, in WriteShapeFromExpressionMap below, must be marked mandatory — `false`,
// not merely "not required," when there's no single backing column at all (the `[X] extends [never]` form,
// rather than a bare `X extends never`, matters here : ReqCol can itself legitimately BE `never`, and a bare
// `never extends never` would then read every entry as required instead of none).
type IsRequiredEntry<E, Rel extends object, ReqCol extends string> = [
  BackingColumnOf<E, Rel>,
] extends [never]
  ? false
  : BackingColumnOf<E, Rel> extends ReqCol
    ? true
    : false

// Each joined member carries its own `relation`/`schema`/`shortcut` fields on its own literal (unlike relation()/
// func()'s root call — see WriteShapeFromQuery's own doc comment) — WriteShapeFromRelationQuery's own ReqCol
// default (RequiredKeysOf<ResolveKey<Q>>) reads its required columns straight off that literal, same as
// ResolveModel<Join[A]> already does for its row shape ; no need to compute or pass it explicitly here.
// Write-side counterpart to JoinMemberShape (above) — same to-one/to-many cardinality split, resolved through
// WriteShapeFromRelationQuery instead of ShapeFromRelationQuery. Shared by WriteJoinShapes and
// WriteShapeFromDotTag, same reasoning as JoinMemberShape's own doc comment.
type WriteJoinMemberShape<J, Depth extends number> =
  JoinCardinality<J> extends true
    ? WriteShapeFromRelationQuery<J, ResolveModel<J>, Depth>
    : WriteShapeFromRelationQuery<J, ResolveModel<J>, Depth>[]

type WriteJoinShapes<Join extends { [name: string]: unknown }, Depth extends number> = {
  [A in keyof Join]: WriteJoinMemberShape<Join[A], Digits[Depth]>
}

// Same physical columns as OwnShape, but a column named in ReqCol (RequiredKeysOf<ResolveKey<Q>>, threaded down
// from wherever this relation was reached in the tree) stays mandatory while everything else becomes optional.
type WriteOwnShape<Rel extends object, ReqCol extends string> = Pick<Rel, ReqCol & keyof Rel> &
  Partial<Omit<Rel, ReqCol>>

type WriteFullShape<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string = never,
> = WriteOwnShape<Rel, ReqCol> & WriteJoinShapes<Join, Depth>

// get is dropped (read-only) ; col/set are writable, same underlying column type as the read side.
type WriteShapeFromFieldTag<
  Tag extends "get" | "col" | "set",
  Rest extends readonly unknown[],
  Rel extends object,
> = Tag extends "get"
  ? Omitted
  : Rest extends readonly [infer Col extends string, ...unknown[]]
    ? Col extends keyof Rel
      ? Rel[Col]
      : unknown
    : unknown

// "."/"dot" on the write side — docs/content/query-language/writing.md ## Writability rules : "a physical
// column is writable ... untransformed except by a coalescing operator, or set/col" and "a computed field ...
// is never a write target" ; server/query/shape.go's own walkSelectForWritability mirrors this at the FoldDot
// case (single-hop only — a multi-hop chain hopping into a child's own column is excluded from writability
// there too). So : a single-hop reference to a real column is writable, same type as col/set (BackingColumnOf,
// above, already counts it toward IsRequiredEntry) ; a single-hop reference to a joined relation recurses into
// that relation's own write shape, same as a "*"-selected join would ; anything else a single hop could name
// (a computed field) or any multi-hop chain (never a real column, whatever it ends in) is Omitted — dropped
// from the write shape entirely, same treatment "call" already gets below.
type WriteShapeFromDotSingleHop<
  Name extends string,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Name extends keyof Rel
  ? Rel[Name]
  : Name extends keyof Join
    ? WriteJoinMemberShape<Join[Name], Depth>
    : Omitted

type WriteShapeFromDotTag<
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Rest extends readonly [infer Name extends string]
  ? WriteShapeFromDotSingleHop<Name, Rel, Join, Depth>
  : Omitted

type WriteOwnFullBase<
  Tag extends OwnFullTag,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string,
> = Tag extends "*~" ? WriteOwnShape<Rel, ReqCol> : WriteFullShape<Rel, Join, Depth, ReqCol>

type WriteShapeFromOwnFullTag<
  Tag extends OwnFullTag,
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string = never,
> =
  WriteOwnFullBase<Tag, Rel, Join, Depth, ReqCol> extends infer Base extends object
    ? Omit<Base, ExceptFromRest<Rest>> &
        WriteShapeFromExpressionMap<AndFromRest<Rest>, Rel, Join, Depth, ReqCol>
    : never

type WriteShapeFromContainerTag<
  Tag extends ContainerTag,
  Items extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string = never,
> = Tag extends "coalesce"
  ? WriteShapeFromExpression<Items[number], Rel, Join, Depth, ReqCol>
  : { [I in keyof Items]: WriteShapeFromExpression<Items[I], Rel, Join, Depth, ReqCol> }

// Same key-remap as ShapeFromExpressionMap, dropping `Omitted` entries (here that's a `get` tag instead of
// `set`) — plus a required/optional split on top, via IsRequiredEntry : a key backed by a column ReqCol names
// (BackingColumnOf) stays mandatory ; every other key, including one with no single backing column at all
// (own/full never reach here — see WriteShapeFromOwnFullTag — but a computed/container value can), is optional.
type WriteShapeFromExpressionMap<
  Obj extends { [name: string]: unknown },
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string = never,
> = Prettify<
  {
    [K in keyof Obj as WriteShapeFromExpression<Obj[K], Rel, Join, Depth, ReqCol> extends Omitted
      ? never
      : IsRequiredEntry<Obj[K], Rel, ReqCol> extends true
        ? never
        : K]?: WriteShapeFromExpression<Obj[K], Rel, Join, Depth, ReqCol>
  } & {
    [K in keyof Obj as WriteShapeFromExpression<Obj[K], Rel, Join, Depth, ReqCol> extends Omitted
      ? never
      : IsRequiredEntry<Obj[K], Rel, ReqCol> extends true
        ? K
        : never]: WriteShapeFromExpression<Obj[K], Rel, Join, Depth, ReqCol>
  }
>

type WriteShapeFromExpression<
  E,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number = 12,
  ReqCol extends string = never,
> = Depth extends 0
  ? unknown
  : E extends readonly [infer Tag extends string, ...infer Rest extends readonly unknown[]]
    ? Tag extends OwnFullTag
      ? WriteShapeFromOwnFullTag<Tag, Rest, Rel, Join, Digits[Depth], ReqCol>
      : Tag extends "get" | "col" | "set"
        ? WriteShapeFromFieldTag<Tag, Rest, Rel>
        : Tag extends "." | "dot"
          ? WriteShapeFromDotTag<Rest, Rel, Join, Digits[Depth]>
          : Tag extends "$param"
            ? ShapeFromParamTag<Rest>
            : Tag extends ContainerTag
              ? WriteShapeFromContainerTag<Tag, Rest, Rel, Join, Digits[Depth], ReqCol>
              : Tag extends "call"
                ? Omitted // never a real column (query-engine.md : "never a candidate for writability") ; dropped
                : unknown // from the write shape entirely, same as `get` above, rather than kept with a nonsensical type.
    : E extends { [name: string]: unknown }
      ? WriteShapeFromExpressionMap<E, Rel, Join, Digits[Depth], ReqCol>
      : ShapeFromLeaf<E>

// A bare top-level `get` has nowhere to drop its own key, same edge case ShapeFromRelationQuery guards against
// for a bare top-level `set` — falls back to `unknown` rather than leaking `Omitted`. ReqCol defaults to
// RequiredKeysOf<ResolveKey<Q>> — Q's OWN `relation`/`schema`/`shortcut` fields, when it carries them (a join
// member's literal, or a Wellknowns entry's embedded query literal) — so a direct caller never has to compute or
// pass it itself. relation()/func() (querier.ts) are the one exception : they split the relation/function name
// out from the request object entirely, so Q alone never carries it — WriteShapeFromQuery's own explicit ReqCol
// parameter exists specifically for relation() to pass RequiredKeysOf<R> in from the name string it still has.
// Prettify wraps every node's own output here too, same reasoning as ShapeFromRelationQuery's own doc comment —
// WriteJoinShapes (above) resolves every nested join member back through this same function.
export type WriteShapeFromRelationQuery<
  Q,
  Rel extends object,
  Depth extends number = 12,
  ReqCol extends string = RequiredKeysOf<ResolveKey<Q>>,
> = Depth extends 0
  ? unknown
  : Prettify<
      MergeProto<
        Q,
        Q extends { select: infer Sel }
          ? [Sel] extends [undefined]
            ? WriteFullShape<Rel, ExtractJoinMap<Q>, Digits[Depth], ReqCol>
            : WriteShapeFromExpression<
                  Sel,
                  Rel,
                  ExtractJoinMap<Q>,
                  Digits[Depth],
                  ReqCol
                > extends infer S
              ? S extends Omitted
                ? unknown
                : S
              : never
          : WriteFullShape<Rel, ExtractJoinMap<Q>, Digits[Depth], ReqCol>,
        true
      >
    >

// Public entry point, mirroring ShapeFromQuery. ReqCol's default mirrors WriteShapeFromRelationQuery's own —
// see that type's doc comment for why relation() (querier.ts) is the one caller that overrides it explicitly.
export type WriteShapeFromQuery<
  Q,
  Rel extends object = { [name: string]: unknown },
  ReqCol extends string = RequiredKeysOf<ResolveKey<Q>>,
> = WriteShapeFromRelationQuery<Q, Rel, 12, ReqCol>
