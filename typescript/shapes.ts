/*!
Type-level machinery : the JSON shape a query produces, and the `$param`s it declares. No runtime code.
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

// Depth budget for the recursive walks, which otherwise hit TS's "excessively deep" limit. Raise it if a real query
// needs more.
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

/**
 Displays an intersection/mapped-type chain as one flat object on hover. Arrays and primitives pass through as-is,
 branded strings (`string & { __pg }`) included : they satisfy `extends object`, and mapping them breaks the brand.
 */
export type Prettify<T> = T extends readonly unknown[]
  ? T
  : T extends string | number | boolean | bigint | symbol
    ? T
    : T extends object
      ? { [K in keyof T]: T[K] } & {}
      : T

/**
 The `$param`s a query declares, as one object ; `unknown` when there are none.
 */
export type Params<Q> = Prettify<UnionToIntersection<ParamUnion<Q>>>

///////////////////////////////////////////////////////////////////////
// ShapeFromQuery : the JSON result shape of a RelationQuery from its `select`/`join`, against Rel (the row type).

export type DefaultRow = { [name: string]: unknown }

/**
 A query node's row type, root or join, from its `shortcut`, `relation` or `function`. DefaultRow when the name is
 unknown or not schema-qualified (no search_path here).
 */
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

/**
 The "schema.table" key of a node's relation, to look up RequiredColumns. `never` for a function node (never
 writable) or an unknown name.
 */
export type ResolveKey<Node> = Node extends { shortcut: infer S extends string }
  ? ParsedShortcut<S>["target"]
  : Node extends { schema: infer Sc extends string; relation: infer R extends string }
    ? `${Sc}.${R}`
    : Node extends { relation: infer R extends string }
      ? R
      : never

// A function's row type : its `relation` when set-returning, else its scalar `returns`. Functions[F] is a union of
// overloads ; DistributeOverload resolves each overload on its own instead of the whole union through one branch.
type ResolveFunctionModel<F extends keyof Functions> = DistributeOverload<Functions[F]>

type DistributeOverload<M> = M extends { relation: infer R extends object }
  ? R
  : M extends { returns: infer Ret }
    ? Ret
    : DefaultRow

///////////////////////////////////////////////////////////////////////
// Function call arguments, from the declared `positional_args`/`args` ; an overloaded function offers each
// overload's arguments.

/**
 call()'s argument type : plain values as declared, no `$param`, since call() runs immediately.
 */
export type FunctionArgs<M> = M extends {
  positional_args: infer P extends readonly unknown[]
  args: infer A extends object
}
  ? P | A
  : never

/**
 func()'s argument type : FunctionArgs where each argument may also be a `["$param", name, cast?]`.
 */
export type DeferredFunctionArgs<M> = M extends {
  positional_args: infer P extends readonly unknown[]
  args: infer A extends object
}
  ? MappedWithParam<P> | MappedWithParam<A>
  : never

type ParamPlaceholder = readonly ["$param", string, string?]

type MappedWithParam<T> = { [K in keyof T]: T[K] | ParamPlaceholder }

/**
 The overload(s) of M whose declared arguments accept `Args`, so call() returns the matched overload's type.
 */
export type MatchOverload<M, Args> = M extends {
  positional_args: infer P
  args: infer A
}
  ? Args extends P | A
    ? M
    : never
  : never

///////////////////////////////////////////////////////////////////////
// Root cardinality : a query root is an array of rows, except a scalar function call, which is its bare value.

/**
 A function root's response : an array of rows for a set-returning overload, the bare `returns` value for a scalar
 one, unioned over overloads. DefaultRow[] for an unknown function (`never`).
 */
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

/**
 Root response shape of a query literal that carries its own `relation`/`function`, such as a well-known query's.
 */
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

// `{}` for "no join" because its keyof is `never` ; biome's suggested replacements would add an index signature.
export type ExtractJoinMap<Q> = Q extends {
  join: infer J extends { [name: string]: RelationQuery }
}
  ? J
  : // biome-ignore lint/complexity/noBannedTypes: see above — `{}` is deliberate here, not a placeholder
    {}

/**
 Parses "schema.relation<marker>referenced_col:referencing_col[,...]" into target, marker and raw pairs ; `never`
 without a marker. `>` is outgoing (this relation owns the foreign key), `<` incoming and unique, `*` incoming and
 multiple. Must match querier.ts's parseShortcut.
 */
// One pattern per marker : a single pattern with an inferred marker resolved to `never` when tried.
export type ParsedShortcut<S extends string> = S extends `${infer Target}>${infer Pairs}`
  ? { target: Target; marker: ">"; pairs: Pairs }
  : S extends `${infer Target}<${infer Pairs}`
    ? { target: Target; marker: "<"; pairs: Pairs }
    : S extends `${infer Target}*${infer Pairs}`
      ? { target: Target; marker: "*"; pairs: Pairs }
      : never

// `*` is to-many ; `<`/`>` are both to-one. A join written out by hand (no `shortcut`) has no cardinality
// source, so it defaults to an array as before.
type JoinCardinality<J> = J extends { shortcut: infer S extends string }
  ? ParsedShortcut<S>["marker"] extends "*"
    ? false
    : true
  : false

type SplitOnComma<S extends string> = S extends `${infer Head},${infer Rest}`
  ? Head | SplitOnComma<Rest>
  : S

type ReferencingColumnOf<Pair extends string> = Pair extends `${string}:${infer Referencing}`
  ? Referencing
  : never

type ReferencingColumnsOfShortcut<S extends string> = ReferencingColumnOf<
  SplitOnComma<ParsedShortcut<S>["pairs"]>
>

type AnyColumnNullable<Cols extends string, Rel extends object> = Cols extends keyof Rel
  ? null extends Rel[Cols]
    ? true
    : never
  : never

// A to-one join is nullable when incoming (`<`), or when outgoing (`>`) and any of its referencing columns is
// nullable (MATCH SIMPLE).
type IsNullableJoin<
  S extends string,
  ParentRel extends object,
> = ParsedShortcut<S>["marker"] extends "<"
  ? true
  : [AnyColumnNullable<ReferencingColumnsOfShortcut<S>, ParentRel>] extends [never]
    ? false
    : true

// To-one is an object (maybe null), to-many an array (never null). `ParentRel` is the declaring relation's row type.
type JoinMemberShape<J, ParentRel extends object, Depth extends number> =
  JoinCardinality<J> extends true
    ? J extends { shortcut: infer S extends string }
      ? IsNullableJoin<S, ParentRel> extends true
        ? ShapeFromRelationQuery<J, ResolveModel<J>, Depth> | null
        : ShapeFromRelationQuery<J, ResolveModel<J>, Depth>
      : ShapeFromRelationQuery<J, ResolveModel<J>, Depth>
    : ShapeFromRelationQuery<J, ResolveModel<J>, Depth>[]

type JoinShapes<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = {
  [A in keyof Join]: JoinMemberShape<Join[A], Rel, Digits[Depth]>
}

type OwnShape<Rel extends object> = { [K in keyof Rel]: Rel[K] }

type FullShape<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = OwnShape<Rel> & JoinShapes<Rel, Join, Depth>

// Marks a key absent on this side (`get` on write, `set` on read). Not `never`, so a column typed `never` is kept.
declare const OmittedTag: unique symbol
type Omitted = { [OmittedTag]: true }

type OwnFullTag = "*" | "*~"

type ContainerTag = "arr" | "array" | "lst" | "list" | "coalesce"

// Primitives and literal strings ; a bare string is always a literal.
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

// "draft" is "write" plus `proto` members : every check tests `M extends "read"`, so it dispatches like "write" ;
// only WriteShapeFromRelationQuery tells them apart, through ProtoFlag.
type Mode = "read" | "write" | "draft"

type ProtoFlag<M extends Mode> = M extends "draft" ? true : false

// `ReqCol` only matters on the write side.
type OwnFullBaseShape<
  M extends Mode,
  Tag extends OwnFullTag,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string,
> = M extends "read"
  ? Tag extends "*~"
    ? OwnShape<Rel>
    : FullShape<Rel, Join, Depth>
  : Tag extends "*~"
    ? WriteOwnShape<Rel, ReqCol>
    : WriteFullShape<Rel, Join, Depth, ReqCol, ProtoFlag<M>>

// Folds "*"/"*~"'s trailing arguments : except-lists into one union, and-maps merged with later keys winning.
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
  : // Not `Record<string, never>` : its keyof `string` would make the Omit above strip every key.
    Record<never, never>

type OwnFullTagShape<
  M extends Mode,
  Tag extends OwnFullTag,
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string,
> =
  OwnFullBaseShape<M, Tag, Rel, Join, Depth, ReqCol> extends infer Base extends object
    ? Omit<Base, ExceptFromRest<Rest>> &
        ExpressionMapShape<M, AndFromRest<Rest>, Rel, Join, Depth, ReqCol>
    : never

// get/col/set : read drops `set`, write drops `get`, `col` is on both sides.
type FieldTagShape<
  M extends Mode,
  Tag extends "get" | "col" | "set",
  Rest extends readonly unknown[],
  Rel extends object,
> = Tag extends (M extends "read" ? "set" : "get")
  ? Omitted
  : Rest extends readonly [infer Col extends string, ...unknown[]]
    ? Col extends keyof Rel
      ? Rel[Col]
      : unknown
    : unknown

// $param : typed from its cast ; without one the server uses jsonb, so `unknown`.
type ShapeFromParamTag<Rest extends readonly unknown[]> = Rest extends readonly [
  string,
  infer V extends keyof TypeMap,
]
  ? TypeMap[V]
  : unknown

// ["call", identifier, ...] : a bare identifier resolves through FunctionsByName (search_path), `{schema,name}`
// through Functions. The value is the function's `returns`, arrays included. Unknown identifier : `unknown`.
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

type ReturnsOf<M> = M extends { returns: infer Ret } ? Ret : unknown

// "."/"dot" first hop : a column, a join, or a computed field. FunctionsByName is not scoped to Rel, so a computed
// field sharing its name with another relation's can be mistyped.
type ShapeFromDotSingleHop<
  Name extends string,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Name extends keyof Rel
  ? Rel[Name]
  : Name extends keyof Join
    ? JoinMemberShape<Join[Name], Rel, Depth>
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

// A leading string is a name looked up in scope, any other leading operand a sub-expression. Must match the
// server's parsing of ".".
type ShapeFromDotTag<
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
> = Rest extends readonly [infer Head, ...infer Hops extends readonly string[]]
  ? Head extends string
    ? ShapeFromDotChain<ShapeFromDotSingleHop<Head, Rel, Join, Depth>, Hops>
    : ShapeFromDotChain<ShapeFromExpression<Head, Rel, Join, Depth>, Hops>
  : unknown

// arr/array/lst/list give a tuple ; coalesce the union of its arguments.
type ContainerTagShape<
  M extends Mode,
  Tag extends ContainerTag,
  Items extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string,
> = Tag extends "coalesce"
  ? ExpressionShape<M, Items[number], Rel, Join, Depth, ReqCol>
  : { [I in keyof Items]: ExpressionShape<M, Items[I], Rel, Join, Depth, ReqCol> }

// Drops entries that resolved to `Omitted` ; the write side also splits required from optional.
type ExpressionMapShape<
  M extends Mode,
  Obj extends { [name: string]: unknown },
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string,
> = M extends "read"
  ? {
      [K in keyof Obj as ExpressionShape<M, Obj[K], Rel, Join, Depth, ReqCol> extends Omitted
        ? never
        : K]: ExpressionShape<M, Obj[K], Rel, Join, Depth, ReqCol>
    }
  : Prettify<
      {
        [K in keyof Obj as ExpressionShape<M, Obj[K], Rel, Join, Depth, ReqCol> extends Omitted
          ? never
          : IsRequiredEntry<Obj[K], Rel, Join, Depth, ReqCol, Obj> extends true
            ? never
            : K]?: ExpressionShape<M, Obj[K], Rel, Join, Depth, ReqCol>
      } & {
        [K in keyof Obj as ExpressionShape<M, Obj[K], Rel, Join, Depth, ReqCol> extends Omitted
          ? never
          : IsRequiredEntry<Obj[K], Rel, Join, Depth, ReqCol, Obj> extends true
            ? K
            : never]: ExpressionShape<M, Obj[K], Rel, Join, Depth, ReqCol>
      }
    >

/**
 The JSON value one Expression produces, read or write. On write, "."/"dot" is single-hop and drops computed
 fields, and "call" is never writable. Operators, "agg", "index"/"slice", "format", ... are `unknown`.
 */
type ExpressionShape<
  M extends Mode,
  E,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string,
> = Depth extends 0
  ? unknown
  : E extends readonly [infer Tag extends string, ...infer Rest extends readonly unknown[]]
    ? Tag extends OwnFullTag
      ? OwnFullTagShape<M, Tag, Rest, Rel, Join, Digits[Depth], ReqCol>
      : Tag extends "get" | "col" | "set"
        ? FieldTagShape<M, Tag, Rest, Rel>
        : Tag extends "." | "dot"
          ? M extends "read"
            ? ShapeFromDotTag<Rest, Rel, Join, Digits[Depth]>
            : WriteShapeFromDotTag<Rest, Rel, Join, Digits[Depth], ProtoFlag<M>>
          : Tag extends "$param"
            ? ShapeFromParamTag<Rest>
            : Tag extends ContainerTag
              ? ContainerTagShape<M, Tag, Rest, Rel, Join, Digits[Depth], ReqCol>
              : Tag extends "call"
                ? M extends "read"
                  ? ShapeFromCallTag<Rest>
                  : Omitted // never writable
                : unknown // any other operator/agg/index/slice/format tuple
    : E extends { [name: string]: unknown }
      ? ExpressionMapShape<M, E, Rel, Join, Digits[Depth], ReqCol>
      : ShapeFromLeaf<E>

type ShapeFromExpression<
  E,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number = 12,
> = ExpressionShape<"read", E, Rel, Join, Depth, never>

type WriteShapeFromExpression<
  E,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number = 12,
  ReqCol extends string = never,
  WithProtoMembers extends boolean = false,
> = ExpressionShape<WithProtoMembers extends true ? "draft" : "write", E, Rel, Join, Depth, ReqCol>

// One node's shape before `proto` merges in ; no `select` means `["*"]`. A bare top-level `set` gives `unknown`.
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

/**
 The row type `this` has inside a `proto` : the node's shape without its own `proto` members.
 */
export type ProtoRowShape<
  Q,
  Rel extends object,
  Depth extends number = 12,
> = BaseShapeFromRelationQuery<Q, Rel, Depth>

// One `proto` entry's member type : a method, or a descriptor (`{get, set}`/`{value}`).
// biome-ignore-start lint/suspicious/noExplicitAny: matching an arbitrary function/setter signature is load-bearing here
// Members are matched as required, so accessor helpers must return `{get(): T; set(v): void}`, never
// `TypedPropertyDescriptor<T>`, whose optional members can't tell get-only from writable.
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

// The members a `proto` descriptor map produces. `-readonly` because a `const`-inferred P makes every key readonly.
type FromDescriptorMap<P> = {
  readonly [K in keyof P as IsWritable<P[K]> extends true ? never : K]: ExtractDescriptor<P[K]>
} & {
  -readonly [K in keyof P as IsWritable<P[K]> extends true ? K : never]: ExtractDescriptor<P[K]>
}

// Merges `proto`'s own members into the row shape it decorates.
type MergeProto<Q, Base> = Q extends {
  proto: infer P extends object
}
  ? Base & FromDescriptorMap<P>
  : Base

/**
 One node's read shape, `proto` members included. Every nested join resolves through it, so every level is
 flattened on hover.
 */
export type ShapeFromRelationQuery<
  Q,
  Rel extends object,
  Depth extends number = 12,
> = Depth extends 0
  ? unknown
  : Prettify<MergeProto<Q, BaseShapeFromRelationQuery<Q, Rel, Digits[Depth]>>>

/**
 Types `this` inside the request's `proto` as the node's row, and rejects unknown keys. A `proto` mixing a `get`
 with a plain method needs an explicit return type on every member, or `this` silently becomes `any`.
 */
// `ThisType`, not a callback : a callback `proto` broke the inference of Q's other fields when tried.
export type WithProto<Q, Rel extends object> = Q & {
  proto?: object & ThisType<ProtoRowShape<NoInfer<Q>, Rel>>
} & NoUnknownKeys<NoInfer<Q>>

/**
 Maps every key of Q that RelationQuery doesn't have to `never`, so a misspelled one (`order` for `order_by`) is
 flagged on that key : Q is inferred from the literal, so excess keys aren't otherwise caught.
 */
export type NoUnknownKeys<Q> = [Exclude<keyof Q, keyof RelationQuery>] extends [never]
  ? unknown
  : { [K in Exclude<keyof Q, keyof RelationQuery>]: never }

/**
 The read shape of query Q against row type Rel. Q is not validated here ; relation()/func()/join() do that (a
 second bound here drifted from theirs).
 */
export type ShapeFromQuery<
  Q,
  Rel extends object = { [name: string]: unknown },
> = ShapeFromRelationQuery<Q, Rel>

///////////////////////////////////////////////////////////////////////
// WriteShapeFromQuery : the `data` shape a write expects. Like ShapeFromQuery, except `get` is dropped and `set`/`col`
// kept. A column in RequiredColumns[key] stays required, also under a renamed select key ; every other column is
// optional. Applies to every relation in the tree.
//
// Left to the server : the exactly-once rule, `insert_columns`/`update_columns`, identity-target writability, and
// write_mode-dependent required columns (an update-only mode still types insert-required columns as required).

/**
 RequiredColumns[key], or `never` when key isn't a known relation.
 */
export type RequiredKeysOf<Key> = Key extends keyof RequiredColumns ? RequiredColumns[Key] : never

// The one column E names : col/set, or a single-hop "."/"dot" (same as the server's writability walk). Anything
// else is `never`, so it can't be required.
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

// The join a single-hop "."/"dot" names.
type BackingJoinOf<E, Join extends { [name: string]: unknown }> = E extends readonly [
  "." | "dot",
  infer Name extends string,
]
  ? Name extends keyof Join
    ? Name
    : never
  : never

// Whether E is a nested object-literal Expression (`{ project: { id: ["col", "id"] } }`), not a tag tuple.
type IsNestedExpressionMap<E> = E extends { [name: string]: unknown }
  ? E extends readonly unknown[]
    ? false
    : true
  : false

// Whether a select map entry is required on write : for a column iff ReqCol names it, for a nested map iff it has a
// required key inside, for a join iff it fills a required column the map doesn't list (an absent join key leaves
// that column to the row). `[X] extends [never]` because ReqCol may itself be `never`.
type IsRequiredEntry<
  E,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string,
  Obj,
> = [BackingJoinOf<E, Join>] extends [never]
  ? [BackingColumnOf<E, Rel>] extends [never]
    ? IsNestedExpressionMap<E> extends true
      ? Record<never, never> extends WriteShapeFromExpression<E, Rel, Join, Depth, ReqCol>
        ? false
        : true
      : false
    : BackingColumnOf<E, Rel> extends ReqCol
      ? true
      : false
  : [
        Exclude<
          RequiredJoinColumns<Join[BackingJoinOf<E, Join>], Rel, ReqCol>,
          { [K in keyof Obj]: BackingColumnOf<Obj[K], Rel> }[keyof Obj]
        >,
      ] extends [never]
    ? false
    : true

// The required columns a non-nullable outgoing (`>`) join fills : a row leaving the join out must give them itself.
// `never` for every other join.
type RequiredJoinColumns<J, Rel extends object, ReqCol extends string> = J extends {
  shortcut: infer S extends string
}
  ? ParsedShortcut<S>["marker"] extends ">"
    ? IsNullableJoin<S, Rel> extends true
      ? never
      : ReferencingColumnsOfShortcut<S> & ReqCol
    : never
  : never

// Required columns of a joined relation, minus the ones the join fills in : on an incoming join (`<`/`*`) the
// server copies the parent's key into the referencing columns.
type JoinRequiredKeys<J> = Exclude<
  RequiredKeysOf<ResolveKey<J>>,
  J extends { shortcut: infer S extends string }
    ? ParsedShortcut<S>["marker"] extends "<" | "*"
      ? ReferencingColumnsOfShortcut<S>
      : never
    : never
>

// Write-side JoinMemberShape.
type WriteJoinMemberShape<
  J,
  ParentRel extends object,
  Depth extends number,
  WithProtoMembers extends boolean = false,
> =
  JoinCardinality<J> extends true
    ? J extends { shortcut: infer S extends string }
      ? IsNullableJoin<S, ParentRel> extends true
        ? WriteShapeFromRelationQuery<
            J,
            ResolveModel<J>,
            Depth,
            JoinRequiredKeys<J>,
            WithProtoMembers
          > | null
        : WriteShapeFromRelationQuery<
            J,
            ResolveModel<J>,
            Depth,
            JoinRequiredKeys<J>,
            WithProtoMembers
          >
      : WriteShapeFromRelationQuery<
          J,
          ResolveModel<J>,
          Depth,
          JoinRequiredKeys<J>,
          WithProtoMembers
        >
    : WriteShapeFromRelationQuery<
        J,
        ResolveModel<J>,
        Depth,
        JoinRequiredKeys<J>,
        WithProtoMembers
      >[]

type WriteJoinShapes<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  WithProtoMembers extends boolean = false,
> = {
  [A in keyof Join]?: WriteJoinMemberShape<Join[A], Rel, Digits[Depth], WithProtoMembers>
}

// For each join filling required columns : the join given, or left out with those columns given instead. An
// intersection of one union per such join ; `unknown` when there is none.
type JoinOrColumns<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string,
  WithProtoMembers extends boolean,
> = IntersectParams<
  {
    [A in keyof Join]: [RequiredJoinColumns<Join[A], Rel, ReqCol>] extends [never]
      ? never
      : (
          x:
            | { [P in A]: WriteJoinMemberShape<Join[A], Rel, Digits[Depth], WithProtoMembers> }
            | ({ [P in A]?: undefined } & {
                [C in RequiredJoinColumns<Join[A], Rel, ReqCol> & keyof Rel]: Rel[C]
              }),
        ) => void
  }[keyof Join]
>

// The intersection of the parameter types of a union of one-parameter functions, `unknown` for none.
type IntersectParams<F> = [F] extends [never]
  ? unknown
  : [F] extends [(x: infer I) => void]
    ? I
    : never

// OwnShape with only the ReqCol columns required.
type WriteOwnShape<Rel extends object, ReqCol extends string> = Pick<Rel, ReqCol & keyof Rel> &
  Partial<Omit<Rel, ReqCol>>

// Columns filled by this relation's non-nullable outgoing (`>`) joins, whose row is written first. Only applies to
// "*" ; a `select` map keeps these columns required.
type OutgoingFilledKeys<Rel extends object, Join extends { [name: string]: unknown }> = {
  [A in keyof Join]: Join[A] extends { shortcut: infer S extends string }
    ? ParsedShortcut<S>["marker"] extends ">"
      ? IsNullableJoin<S, Rel> extends true
        ? never
        : ReferencingColumnsOfShortcut<S>
      : never
    : never
}[keyof Join]

type WriteFullShape<
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  ReqCol extends string = never,
  WithProtoMembers extends boolean = false,
> = WriteOwnShape<Rel, Exclude<ReqCol, OutgoingFilledKeys<Rel, Join>>> &
  WriteJoinShapes<Rel, Join, Depth, WithProtoMembers> &
  JoinOrColumns<Rel, Join, Depth, ReqCol, WithProtoMembers>

// "."/"dot" on write : a single hop to a column or join is writable ; a computed field or a multi-hop chain is
// Omitted. Must match the server's writability walk.
type WriteShapeFromDotSingleHop<
  Name extends string,
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  WithProtoMembers extends boolean = false,
> = Name extends keyof Rel
  ? Rel[Name]
  : Name extends keyof Join
    ? WriteJoinMemberShape<Join[Name], Rel, Depth, WithProtoMembers>
    : Omitted

type WriteShapeFromDotTag<
  Rest extends readonly unknown[],
  Rel extends object,
  Join extends { [name: string]: unknown },
  Depth extends number,
  WithProtoMembers extends boolean = false,
> = Rest extends readonly [infer Name extends string]
  ? WriteShapeFromDotSingleHop<Name, Rel, Join, Depth, WithProtoMembers>
  : Omitted

/**
 One node's write shape. ReqCol defaults to the required columns of the relation Q names itself. A bare top-level
 `get` gives `unknown`.
 */
export type WriteShapeFromRelationQuery<
  Q,
  Rel extends object,
  Depth extends number = 12,
  ReqCol extends string = RequiredKeysOf<ResolveKey<Q>>,
  WithProtoMembers extends boolean = false,
> = Depth extends 0
  ? unknown
  : Prettify<
      WithProtoMembers extends true
        ? MergeProto<Q, WriteBaseShape<Q, Rel, Depth, ReqCol, true>>
        : WriteBaseShape<Q, Rel, Depth, ReqCol, false>
    >

// WriteShapeFromRelationQuery's node shape before any `proto` is merged in.
type WriteBaseShape<
  Q,
  Rel extends object,
  Depth extends number,
  ReqCol extends string,
  WithProtoMembers extends boolean,
> = Q extends { select: infer Sel }
  ? [Sel] extends [undefined]
    ? WriteFullShape<Rel, ExtractJoinMap<Q>, Digits[Depth], ReqCol, WithProtoMembers>
    : WriteShapeFromExpression<
          Sel,
          Rel,
          ExtractJoinMap<Q>,
          Digits[Depth],
          ReqCol,
          WithProtoMembers
        > extends infer S
      ? S extends Omitted
        ? unknown
        : S
      : never
  : WriteFullShape<Rel, ExtractJoinMap<Q>, Digits[Depth], ReqCol, WithProtoMembers>

/**
 The write shape of query Q against row type Rel ; ReqCol defaults to the required columns of the relation Q names.
 */
export type WriteShapeFromQuery<
  Q,
  Rel extends object = { [name: string]: unknown },
  ReqCol extends string = RequiredKeysOf<ResolveKey<Q>>,
> = WriteShapeFromRelationQuery<Q, Rel, 12, ReqCol>

/**
 What `write()` takes on a relation root, which depends on the query's `write_mode` : `delete` takes no data at all
 (`void`, not `undefined`, so `.write()` can be called with no argument) ; `update` takes ONE patch object holding
 just the columns to change ; every other mode takes an array of rows.
 */
export type WriteDataFromQuery<
  Q,
  Rel extends object = { [name: string]: unknown },
  ReqCol extends string = RequiredKeysOf<ResolveKey<Q>>,
> = Q extends { write_mode: "delete" }
  ? // biome-ignore lint/suspicious/noConfusingVoidType: only void lets a parameter be omitted
    void
  : Q extends { write_mode: "update" }
    ? Partial<WriteShapeFromQuery<Q, Rel, ReqCol>>
    : WriteShapeFromQuery<Q, Rel, ReqCol>[]

/**
 The row `draft()` returns : WriteShapeFromQuery plus every `proto` member, the joins' included. `proto` members are
 never in WriteShapeFromQuery : a key in a literal would shadow the accessor and be sent as an unknown field.
 */
export type DraftShapeFromQuery<
  Q,
  Rel extends object = { [name: string]: unknown },
  ReqCol extends string = RequiredKeysOf<ResolveKey<Q>>,
> = WriteShapeFromRelationQuery<Q, Rel, 12, ReqCol, true>

///////////////////////////////////////////////////////////////////////
// draft() : what `Querier.draft()` returns, and how it rejects fields the write shape doesn't have.
//
// `Row` is the query's own write-shape row (WriteShapeFromQuery, one array level unwrapped) ; `D` is the type
// the caller's literal was inferred as. Every key `D` actually carries becomes required in the result, with
// `Row`'s own declared type for it ; every other key stays exactly as `Row` has it. Joins are followed (an
// object for a to-one join, an array for a to-many one), so a nested row gets the same treatment.

type Primitive = string | number | boolean | bigint | symbol | null | undefined

// A nested value worth descending into : a plain row, not a column value that happens to be an object (a
// `Temporal.Instant`-like class has methods ; a JSON column typed as a record has an index signature).
// Neither can carry optional columns of its own, so leaving them alone loses nothing.
type IsRowObject<R> = R extends Primitive
  ? false
  : R extends object
    ? string extends keyof R
      ? false
      : [Extract<R[keyof R], (...args: never[]) => unknown>] extends [never]
        ? true
        : false
    : false

// Keys of `D` that are really there : a key whose type admits `undefined` (explicitly `undefined`, or missing
// from some element of an array literal) doesn't count as provided. `null` is a real value and does.
type ProvidedKeys<D, R> = {
  [K in keyof D & keyof R]-?: undefined extends D[K] ? never : K
}[keyof D & keyof R]

type TightenValue<R, D> = R extends Primitive
  ? R
  : R extends readonly (infer RE)[]
    ? [D] extends [readonly (infer DE)[]]
      ? // `[]` infers its element as `never`, and `keyof never` is every key : nothing was supplied, so keep R as is
        [DE] extends [never]
        ? R
        : TightenRow<RE, DE, true>[]
      : R
    : IsRowObject<R> extends true
      ? [D] extends [object]
        ? TightenRow<R, D, true>
        : R
      : R

// `Nested` is false for the root row only : a root row may legitimately carry `proto` methods, which
// IsRowObject would otherwise read as "not a plain row".
type TightenRow<R, D, Nested extends boolean = false> = Nested extends true
  ? IsRowObject<R> extends true
    ? TightenFields<R, D>
    : R
  : TightenFields<R, D>

type TightenFields<R, D> = Prettify<
  {
    [K in keyof R as K extends ProvidedKeys<D, R> ? K : never]-?: TightenValue<
      Exclude<R[K], undefined>,
      D[K & keyof D]
    >
  } & {
    [K in keyof R as K extends ProvidedKeys<D, R> ? never : K]: R[K]
  }
>

/** The root row of a write shape (`WriteShape` is array-wrapped at the root). */
export type WriteRowOf<W> = W extends readonly (infer Row)[] ? Row : W

/** What `Querier.draft()` returns for a `data` literal of type `D` : see the comment block above. */
export type DraftResult<W, D> = TightenRow<WriteRowOf<W>, D>

type StrictValue<R, D> = [Exclude<R, null | undefined>] extends [infer RV]
  ? RV extends readonly (infer RE)[]
    ? [D] extends [readonly (infer DE)[]]
      ? StrictFields<RE, DE>[]
      : unknown
    : IsRowObject<RV> extends true
      ? [D] extends [object]
        ? StrictFields<RV, D>
        : unknown
      : unknown
  : unknown

// Mapping a key `Row` doesn't have to `never` is what makes TS flag *that key* when `D & Strict<...>` is
// checked : generic parameters don't get excess-property checks on their own.
type StrictFields<R, D> = {
  [K in keyof D]: K extends keyof R ? StrictValue<R[K], D[K]> : never
}

/** Intersected with `data`'s own type in draft()'s signature to reject keys the write shape doesn't have, at every level. */
export type DraftStrict<W, D> = StrictFields<WriteRowOf<W>, D>

///////////////////////////////////////////////////////////////////////
// draftNested() : addressing a row that sits inside a query's join tree.

/**
 Every path of join aliases through Q's join tree, as a tuple (`["rooms"]`, `["rooms", "beds"]`, ...). Built from
 the query's own `join` map, not from the row type, so only join aliases are offered, never a column. Depth-limited
 because a self-join tree is otherwise unbounded.
 */
export type JoinPaths<Q, Depth extends number = 6> = Depth extends 0
  ? never
  : {
      [A in keyof ExtractJoinMap<Q> & string]:
        | [A]
        | [A, ...JoinPaths<ExtractJoinMap<Q>[A], Digits[Depth]>]
    }[keyof ExtractJoinMap<Q> & string]

// One step down a join : the row inside a to-many array, or inside a nullable to-one.
type JoinedRow<T> = NonNullable<T> extends readonly (infer E)[] ? E : NonNullable<T>

/** The row reached by following the join aliases in `P` from row `R`. */
export type RowAtPath<R, P extends readonly unknown[]> = P extends readonly [infer K, ...infer Rest]
  ? K extends keyof R
    ? RowAtPath<JoinedRow<R[K]>, Rest>
    : never
  : R
