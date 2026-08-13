import { Dayjs } from "dayjs"
import * as s from "@salesway/scotty"

export function query<_T>(
  tinysql: string,
  body?: unknown,
  method: "GET" | "POST" | "PUT" | "DELETE" = "POST",
): Promise<unknown> {
  const init: RequestInit = {
    method,
    headers: {
      "X-Query": encodeURIComponent(tinysql.replaceAll("\\", "\\\\").replaceAll("\n", "\\n")),
    },
    credentials: "include",
  }

  if (body != null) {
    init.body = JSON.stringify(body)
  }

  return fetch(`/rel`, init).then((res) => {
    if (res.status < 400) {
      return res.json()
    } else {
      return Promise.reject(res)
    }
  })
}

/** Call a function on the server. */
export function call<T>(
  name: string,
  args_serializer: s.Serializer<unknown>,
  serializer: s.Serializer<T>,
  args?: unknown,
): Promise<T> {
  return query(`${name}()`, args_serializer.serialize(args)).then(serializer.deserialize)
}

export function sql_value(value: unknown): string {
  if (typeof value === "string") {
    return `'${value}'`
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return `${value}`
  }
  if (value == null) {
    return "null"
  }
  if (value instanceof Date) {
    return `'${value.toISOString()}'`
  }
  if (value instanceof Dayjs) {
    return `'${value.format("YYYY-MM-DDTHH:mm:ssZ")}'`
  }
  return `$$${JSON.stringify(value)}$$`
}

function _ts2sql(arr: TemplateStringsArray | string, ...args: unknown[]): string {
  if (typeof arr === "string") {
    return arr
  }
  return arr
    .map((str, i) => {
      return str + (i < args.length ? sql_value(args[i]) : "")
    })
    .join("")
}

/**
 * Small helper to define getters on a class.
 * Don't forget to declare module { interface { ... newprops } } as well.
 */
export function define<T>(kls: new () => T, props: ThisType<T> & { [name in keyof T]?: T[name] }) {
  Object.defineProperties(kls.prototype, Object.getOwnPropertyDescriptors(props))
}

export const Multiple = true as const
export const OneRow = false as const

export type Model = {
  name: string
  fields: { [key: string]: s.Serializer<unknown> }
  computed_fields?: { [key: string]: s.Serializer<unknown> }
  relations: () => { [key: string]: Relation<Model, true | false> }
  pk?: string[]
  // relations: { [key: string]: Model }
}

export type Props = Resulter<unknown> | PropsSelector<Model, unknown>

export type ResultOfProps<P> =
  P extends Resulter<infer T>
    ? s.UnderlyingType<T>
    : P extends PropsSelector<Model, infer R>
      ? s.UnderlyingType<R>
      : { [name in keyof P]: ResultOfProps<P[name]> }

export abstract class Resulter<T> {
  abstract build(): string
  abstract serializer(): s.Serializer<T>
  abstract init(): T // gives out the default value if it can be given

  clone() {
    const clone = Object.assign(Object.create(this.constructor.prototype), this)
    return clone
  }
}

/** A simple requested property */
export class BaseProp<T> extends Resulter<T> {
  constructor(
    public name: string,
    public _serializer: s.Serializer<T>,
  ) {
    super()
  }

  serializer(): s.Serializer<T> {
    return this._serializer
  }

  build(): string {
    return this.name
  }

  init(v?: T): T {
    return v ?? (undefined as T)
  }
}

type UnArray<T> = T extends (infer U)[] ? U : T

export class PropsSelector<M extends Model, Result> extends Resulter<Result> {
  constructor(
    public model: M,
    public props: { [name: string]: Props },
  ) {
    super()
  }

  _proto: unknown
  withProto<R>(proto: R & ThisType<Result & R>): PropsSelector<M, R & Result> {
    const c = this.clone()
    c._proto = proto
    return c as unknown as PropsSelector<M, R & Result>
  }

  withClass<R>(
    cls: (Super: new (r: UnArray<Result>) => UnArray<Result>) => new (r: UnArray<Result>) => R,
  ): PropsSelector<M, R> {
    class Cls {
      constructor(r: UnArray<Result>) {
        Object.assign(this, r)
      }
    }
    return this.withProto(
      cls(Cls as unknown as new (r: UnArray<Result>) => UnArray<Result>).prototype as R & ThisType<R & Result>,
    )
  }

  computed<K extends Extract<keyof M["computed_fields"], string>>(
    ...fields: K[]
  ): Selector<M, Result & { [Key in K]: s.UnderlyingType<M["computed_fields"][Key]> }> {
    const c = this.clone()
    for (const f of fields) {
      const field = this.model.computed_fields?.[f]
      if (field == null) {
        throw new Error(`computed field ${f} not found`)
      }
      c.props[f] = new BaseProp(f, field)
    }
    return c as unknown as Selector<M, Result & { [Key in K]: s.UnderlyingType<M["computed_fields"][Key]> }>
  }

  omit<K extends Extract<keyof M["fields"], string>>(...fields: K[]): PropsSelector<M, Omit<Result, K>> {
    const c = this.clone()
    for (const f of fields) {
      delete c.props[f]
    }
    return c as unknown as PropsSelector<M, Omit<Result, K>>
  }

  build(): string {
    if (Object.keys(this.props).length === 0) {
      return "{ * }"
    }

    // missing where / order / limit / offset
    return (
      "{ " +
      Object.entries(this.props)
        .map(([name, prop]) => {
          const pro = prop.build()
          if (pro === name) {
            return name
          }
          return `${name}: ${prop.build()}`
        })
        .join(", ") +
      " }"
    )
  }

  _serializer: s.Serializer<Result> | null = null
  serializer(): s.Serializer<Result> {
    // return s.instanceOf(Object, this.props)
    if (this._serializer != null) {
      return this._serializer
    }

    let ser: s.Serializer<unknown>
    if (Object.keys(this.props).length === 0) {
      ser = this._proto != null ? s.instanceOf(this._proto, this.model.fields) : s.obj(this.model.fields)
    } else {
      const props: { [name: string]: s.Serializer<unknown> } = {}
      for (const [name, prop] of Object.entries(this.props)) {
        props[name] = prop.serializer()
        if (prop instanceof RelationSelector && !prop.rel?.multiple) {
          props[name] = props[name].nullable
        }
      }
      ser = this._proto != null ? s.instanceOf(this._proto, props) : (s.obj(props) as s.Serializer<Result>)
    }

    this._serializer = ser
    return ser
  }

  /** Instantiate a new result */
  init(r?: ResultPartial<Result>): Result {
    const obj =
      this._proto != null ? Object.create(this._proto) : ({} as Result) /* FIXME: Object.create(this.instance) */

    const entries = Object.entries(this.props)
    if (entries.length === 0) {
      for (const name of Object.keys(this.model.fields)) {
        ;(obj as Record<keyof Result, unknown>)[name as keyof Result] =
          r?.[name as keyof Result] ?? undefined /* FIXME use default value if it has one ! */
      }
    } else {
      for (const [name, prop] of Object.entries(this.props)) {
        obj[name as keyof Result] = prop.init(r?.[name as keyof Result] ?? undefined)
      }
    }

    return obj
  }
}

/** A Selector that requests from a relation */
export class Selector<M extends Model, Result> extends Resulter<Result> {
  // If name is provided, it will be used instead of the model's name. Usually it will be because it is used in a relation.
  constructor(
    public model: M,
    public props: PropsSelector<M, Result>,
  ) {
    super()
  }

  delete(instance: Result): Promise<Result> {
    if (!this.model.pk) {
      throw new Error("Model has no primary key and can't be deleted")
    }
    const ser = this.serializer().serialize(instance) as Record<string, unknown>
    return query(
      `delete ${this.model.name} where ${this.model.pk?.map((name) => `${name} = ${ser[name]}`).join(" and ")}`,
    ) as Promise<Result>
  }

  save(instance: Result, merge?: boolean): Promise<Result>
  save(instances: Result[], merge?: boolean): Promise<Result[]>
  save(instance: Result | Result[], merge = false): Promise<Result | Result[]> {
    const ser = this.serializer()
    return query(
      `${merge ? "merge" : "upsert"} $body into ${this.model.name}${this.build()}`,
      Array.isArray(instance) ? instance.map(ser.serialize) : ser.serialize(instance),
    ).then((res: unknown) =>
      Array.isArray(instance) ? (res as unknown[]).map(ser.deserialize) : ser.deserialize((res as unknown[])[0]),
    )
  }

  build(): string {
    return `${
      this._where ? ` where ${this._where}` : ""
    } ${this.props.build()} ${this._order ? ` order ${this._order}` : ""}`
  }

  serializer(): s.Serializer<Result> {
    return this.props.serializer()
  }

  create(r?: ResultPartial<Result>, opts = { dry_run: false } as { dry_run?: boolean }): Promise<Result> {
    const inst = this.props.init(r)
    return query(`${opts.dry_run ? "rollback " : ""}upsert $body into ${this.model.name}${this.build()}`, inst).then(
      (res: unknown) => this.serializer().deserialize((res as unknown[])[0]),
    )
  }

  init(r?: ResultPartial<Result>): Result {
    return this.props.init(r)
  }

  _where: string = ""
  where(tpl: TemplateStringsArray, ...args: unknown[]): this
  where(where: string): this
  where(where: string | TemplateStringsArray, ...args: unknown[]): this {
    const c = this.clone()
    if (typeof where === "string") {
      c._where = where
    } else {
      c._where = _ts2sql(where, ...args)
    }
    return c
  }

  _order: string = ""
  order(tpl: TemplateStringsArray, ...args: unknown[]): this
  order(order: string): this
  order(order: string | TemplateStringsArray, ...args: unknown[]): this {
    const c = this.clone()
    if (typeof order === "string") {
      c._order = order
    } else {
      c._order = _ts2sql(order, ...args)
    }
    return c
  }

  computed<K extends Extract<keyof M["computed_fields"], string>>(
    ...fields: K[]
  ): Selector<M, Result & { [Key in K]: s.UnderlyingType<M["computed_fields"][Key]> }> {
    const c = this.clone()
    c.props = c.props.computed(...fields)
    return c as unknown as Selector<M, Result & { [Key in K]: s.UnderlyingType<M["computed_fields"][Key]> }>
  }

  async fetch(): Promise<Result[]> {
    return query(this.model.name + this.build()).then(this.serializer().array.deserialize)
  }

  withProto<R>(
    proto: R & ThisType<UnArray<Result> & R>,
  ): Selector<M, Result extends unknown[] ? (UnArray<Result> & R)[] : Result & R> {
    this.props = this.props.withProto(proto as unknown as R & ThisType<Result & R>)
    return this as unknown as Selector<M, Result extends unknown[] ? (UnArray<Result> & R)[] : Result & R>
  }

  withClass<R>(
    cls: (Super: new (r: UnArray<Result>) => UnArray<Result>) => new (r: UnArray<Result>) => R,
  ): Selector<M, Result extends unknown[] ? R[] : R> {
    class Cls {
      constructor(r: UnArray<Result>) {
        Object.assign(this, r)
      }
    }
    return this.withProto(
      cls(Cls as unknown as new (r: UnArray<Result>) => UnArray<Result>).prototype as R & ThisType<UnArray<Result> & R>,
    )
  }
}

export class RelationSelector<M extends Model, Result> extends Selector<M, Result> {
  constructor(
    model: M,
    selector: PropsSelector<M, Result>,
    public rel_name?: string,
    public rel?: Relation<Model, true | false>,
  ) {
    super(model, selector)
  }

  override serializer(): s.Serializer<Result> {
    let ser = this.props.serializer() as s.Serializer<Result>
    if (this.rel?.multiple) {
      ser = ser.array as unknown as s.Serializer<Result>
    }
    return ser
  }

  override build(): string {
    return `${(this._readonly ? "readonly " : "") + this.rel_name} ${super.build()}`
  }

  _readonly = false
  get readonly() {
    this._readonly = true
    return this
  }

  override init(r?: ResultPartial<Result>): Result {
    if (r != null) {
      if (this.rel?.multiple) {
        if (!Array.isArray(r)) {
          throw new Error("multiple relation should be initialized with an array")
        }
        return r.map((item) => super.init(item)) as unknown as Result
      }
      return super.init(r)
    }
    if (this.rel?.multiple) {
      return [] as unknown as Result
    }
    return null as Result
  }

  asMap<K, SimpleResult>(
    this: RelationSelector<M, SimpleResult[]>,
    ex: (item: SimpleResult) => K,
  ): RelationSelector<M, Map<K, SimpleResult>> {
    return new RelationSelectorMap(
      ex,
      this.model,
      this.props as unknown as PropsSelector<M, Map<K, SimpleResult>>,
      this.rel_name,
      this.rel,
    )
  }
}

export class RelationSelectorMap<M extends Model, K, Result> extends RelationSelector<M, Map<K, Result>> {
  constructor(
    public extractor: (item: Result) => K,
    model: M,
    selector: PropsSelector<M, Map<K, Result>>,
    rel_name?: string,
    rel?: Relation<Model, true | false>,
  ) {
    super(model, selector, rel_name, rel)
  }

  override serializer(): s.Serializer<Map<K, Result>> {
    const ser = this.props.serializer() as s.Serializer<Result>
    const arr = ser.array
    return arr.wrap(
      (json) => new Map(json.map((j) => [this.extractor(j), j])),
      (instance) => Array.from(instance.values()),
    )
  }

  override init(r?: Map<K, ResultPartial<Result>>): Map<K, Result> {
    if (r == null) {
      return new Map<K, Result>()
    }
    return new Map<K, Result>(r.entries().map(([k, v]) => [k, this.props.init(v) as Result]))
  }
}

export class RowBuilder<M extends Model> {
  constructor(public model: M) {}

  get all(): PropsSelector<M, { [name in keyof M["fields"]]: s.UnderlyingType<M["fields"][name]> }> {
    return new PropsSelector(
      this.model,
      Object.fromEntries(Object.entries(this.model.fields).map(([name, field]) => [name, new BaseProp(name, field)])),
    ) as unknown as PropsSelector<M, { [name in keyof M["fields"]]: s.UnderlyingType<M["fields"][name]> }>
  }

  fields<K extends keyof M["fields"] | keyof M["computed_fields"]>(
    ...fields: K[]
  ): PropsSelector<
    M,
    {
      [name in K]: name extends keyof M["computed_fields"]
        ? s.UnderlyingType<M["computed_fields"][name]>
        : s.UnderlyingType<M["fields"][name]>
    }
  > {
    return new PropsSelector(
      this.model,
      Object.fromEntries(fields.map((name) => [name, this.field(name)])) as { [name: string]: Props },
    ) as unknown as PropsSelector<
      M,
      {
        [name in K]: name extends keyof M["computed_fields"]
          ? s.UnderlyingType<M["computed_fields"][name]>
          : s.UnderlyingType<M["fields"][name]>
      }
    >
  }

  field<K extends keyof M["fields"] | keyof M["computed_fields"]>(
    name: K,
  ): K extends keyof M["fields"]
    ? BaseProp<M["fields"][K]>
    : K extends keyof M["computed_fields"]
      ? BaseProp<M["computed_fields"][K]>
      : never {
    if (name in this.model.fields) {
      const field = this.model.fields[name as string]
      if (field == null) {
        throw new Error(`field ${String(name)} not found`)
      }
      return new BaseProp(name as string, field) as unknown as K extends keyof M["fields"]
        ? BaseProp<M["fields"][K]>
        : K extends keyof M["computed_fields"]
          ? BaseProp<M["computed_fields"][K]>
          : never
    }
    const field = this.model.computed_fields?.[name as string]
    if (field == null) {
      throw new Error(`computed field ${String(name)} not found`)
    }
    return new BaseProp(name as string, field) as unknown as K extends keyof M["fields"]
      ? BaseProp<M["fields"][K]>
      : K extends keyof M["computed_fields"]
        ? BaseProp<M["computed_fields"][K]>
        : never
  }

  rel<
    K extends Extract<keyof ReturnType<M["relations"]>, string>,
    M2 extends ReturnType<M["relations"]>[K]["model"],
    Res,
    R extends ReturnType<M["relations"]>[K] = ReturnType<M["relations"]>[K],
  >(
    name: K,
    sel: Selector<M2, Res> | PropsSelector<M2, Res>,
  ): true extends R["multiple"] ? RelationSelector<M2, Res[]> : RelationSelector<M2, Res>
  rel<
    K extends Extract<keyof ReturnType<M["relations"]>, string>,
    Res,
    R extends ReturnType<M["relations"]>[K] = ReturnType<M["relations"]>[K],
  >(
    name: K,
    cbk: (s: RowBuilder<R["model"]>) => Res,
  ): RelationSelector<R["model"], true extends R["multiple"] ? ResultOfProps<Res>[] : ResultOfProps<Res>>
  rel<
    K extends Extract<keyof ReturnType<M["relations"]>, string>,
    R extends ReturnType<M["relations"]>[K] = ReturnType<M["relations"]>[K],
    Res extends R["model"]["fields"] = R["model"]["fields"],
  >(name: K): RelationSelector<R["model"], true extends R["multiple"] ? Deserialized<Res>[] : Deserialized<Res>>
  rel<
    K extends Extract<keyof ReturnType<M["relations"]>, string>,
    R extends ReturnType<M["relations"]>[K] = ReturnType<M["relations"]>[K],
    Res extends R["model"]["fields"] = R["model"]["fields"],
  >(
    name: K,
    cbk?: ((s: RowBuilder<R["model"]>) => Res) | Selector<R["model"], Res> | PropsSelector<R["model"], Res>,
  ): unknown {
    const rel = this.model.relations()[name as keyof ReturnType<M["relations"]>]

    const res =
      cbk instanceof Selector
        ? cbk.props
        : cbk instanceof PropsSelector
          ? cbk
          : (cbk?.(new RowBuilder(rel.model)) ?? new PropsSelector(rel.model, {}))

    return new RelationSelector(
      rel.model,
      ensure_selector(
        rel.model,
        res as unknown as Resulter<unknown> | { [name: string]: Resulter<unknown> },
      ) as unknown as PropsSelector<R["model"], unknown>,
      name,
      rel,
    )
  }
}

function ensure_selector<M extends Model>(model: M, res: Resulter<unknown> | { [name: string]: Resulter<unknown> }) {
  if (res instanceof Resulter) {
    return res
  }

  const props: { [name: string]: Resulter<unknown> } = {}

  if (typeof res !== "object") {
    throw new Error("Invalid selector")
  }

  for (const [name, prop] of Object.entries(res)) {
    props[name] = ensure_selector(model, prop)
  }
  return new PropsSelector(model, props)
}

type Deserialized<T> = { [name in keyof T]: s.UnderlyingType<T[name]> }

/**
 * Select from a model
 */
export function select<M extends Model, R>(mod: M, cbk: (s: RowBuilder<M>) => R): Selector<M, ResultOfProps<R>>
export function select<M extends Model>(mod: M): Selector<M, Deserialized<M["fields"]>>
export function select<M extends Model, R>(mod: M, cbk?: (s: RowBuilder<M>) => R): unknown {
  if (cbk == null) {
    return new Selector(mod, new PropsSelector(mod, {}))
  }
  const builder = new RowBuilder(mod)
  const res = cbk(builder)
  return new Selector(
    mod,
    ensure_selector(
      mod,
      res as unknown as Resulter<unknown> | { [name: string]: Resulter<unknown> },
    ) as unknown as PropsSelector<M, ResultOfProps<R>>,
  )
}

//-----------------------
export function Rel<M extends Model, Mul extends true | false>(
  model: M,
  multiple: Mul,
  columns: string[],
  distant_columns?: string[],
) {
  return { model, multiple, columns, distant_columns }
}

export type Relation<M extends Model, Mul extends true | false> = {
  model: M
  multiple: Mul
  columns: string[]
  distant_columns?: string[]
}

export type ResultType<S> =
  S extends Resulter<infer T>
    ? T
    : S extends PropsSelector<Model, infer R>
      ? R
      : { [name in keyof S]: ResultType<S[name]> }

export type ResultPartial<T> = {
  // Definition that I would like :
  [P in keyof T]?: T[P] extends (infer U)[]
    ? { [index: number]: U | ResultPartial<U> }
    : T[P] extends object
      ? T[P] | ResultPartial<T[P]>
      : T[P]
}
