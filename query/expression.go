package query

// Expression is implemented by every node produced by parsing query.ts's
// `Expression` grammar (see ParseExpression in expression_parse.go). This is
// pass 1's output for expression-typed fields (where/select/order_by/
// distinct_on/function arguments) : purely syntactic — bare strings stay
// opaque (Identifier), nothing here does DB or scope lookups. Folded
// operators are the one exception : folding ([FoldedOperator, ...Expression])
// into nested binary FoldedExpr nodes is pure syntax, independent of scope,
// so it happens at parse time rather than being deferred.
type Expression interface {
	// Validate is a placeholder ; every Validate() below is a no-op, not the
	// real check — don't rely on it catching anything yet.
	//
	//> Question: this signature was already flagged unsettled in the stub
	//> this file started from ("may need arguments?") — pass 2's real
	//> scope-resolution walk will very likely need a scope/context argument
	//> this doesn't have. Confirm the real signature once pass 2 is designed.
	Validate() error
}

// notYetValidated is embedded by every node type below to satisfy Expression
// without repeating the same no-op Validate on each one. See Expression's
// doc comment : this is a placeholder, not real validation.
type notYetValidated struct{}

func (notYetValidated) Validate() error { return nil }

// ---- order by ---------------------------------------------------------------

type OrderByDirection int

const (
	OrderAsc OrderByDirection = iota
	OrderDesc
	OrderAscNullsFirst
	OrderDescNullsLast
)

// OrderByTerm is one entry of query.ts's order_by array : a bare Expression
// defaults to OrderAsc (SQL's own default, nulls last), or an explicit
// [direction, Expression] pair.
type OrderByTerm struct {
	Expr      Expression
	Direction OrderByDirection
}

// ---- literals / atoms --------------------------------------------------------

// NullLiteral is JSON null.
type NullLiteral struct{ notYetValidated }

// BoolLiteral is JSON true/false.
type BoolLiteral struct {
	notYetValidated
	Value bool
}

// NumberLiteral is a bare JSON number. Arbitrary-precision values use
// BigIntLiteral/NumericLiteral instead — query.ts's explicit escape hatch for
// values a float64 can't carry losslessly.
type NumberLiteral struct {
	notYetValidated
	Value float64
}

// Star is the literal "*" string : "select all fields of the current
// relation + aliases". Distinct from Identifier despite both parsing from a
// bare JSON string — it means something structurally different (a
// select-shape wildcard, not a reference to one named thing).
type Star struct{ notYetValidated }

// Identifier is a bare JSON string other than "*" : a reference to a column
// name or an alias, per query.ts : "strings always refer to aliases and
// column names, since they are much more likely to appear than actual
// strings". Left unresolved (not yet known to be a column vs. an alias vs.
// invalid) until pass 2.
type Identifier struct {
	notYetValidated
	Name string
}

// StringLiteral is query.ts's one-element-array escape hatch for an actual
// string value : [string].
type StringLiteral struct {
	notYetValidated
	Value string
}

// BigIntLiteral is ["bigint", value].
type BigIntLiteral struct {
	notYetValidated
	Value string
}

// NumericLiteral is ["numeric", value] : for values too large/precise for
// NumberLiteral's float64.
type NumericLiteral struct {
	notYetValidated
	Value string
}

// DefaultKeyword is the literal "default" keyword, valid only in GetExpr's /
// SetExpr's / GetSetExpr's default-value positions (querying.md : "The
// default expression may be the 'default' keyword if the column has a
// default value") — meaning "use the column's own DB-level default
// expression", not a reference to a column literally named default. Parsed
// as this sentinel only in that specific position ; a bare "default" string
// anywhere else in Expression position is an ordinary Identifier, same as any
// other name.
//
// > Question: this is my interpretation of that querying.md comment, not a
// > confirmed spec point — confirm this is actually what was meant.
type DefaultKeyword struct{ notYetValidated }

// ---- operators ----------------------------------------------------------------

type UnaryOperator string

const (
	UnaryNeg        UnaryOperator = "-"
	UnaryNot        UnaryOperator = "not"
	UnaryBitNot     UnaryOperator = "~"
	UnaryIsNull     UnaryOperator = "is-null"
	UnaryIsTrue     UnaryOperator = "is-true"
	UnaryIsFalse    UnaryOperator = "is-false"
	UnaryIsNotNull  UnaryOperator = "is-not-null"
	UnaryIsNotTrue  UnaryOperator = "is-not-true"
	UnaryIsNotFalse UnaryOperator = "is-not-false"
	UnarySqrt       UnaryOperator = "|/"
	UnaryCubeRoot   UnaryOperator = "||/"
)

// UnaryExpr is [UnaryOperator, Expression].
type UnaryExpr struct {
	notYetValidated
	Op   UnaryOperator
	Expr Expression
}

type BinaryOperator string

const (
	BinaryLike            BinaryOperator = "like"
	BinaryILike           BinaryOperator = "ilike"
	BinaryRegexMatch      BinaryOperator = "~"
	BinaryRegexMatchCI    BinaryOperator = "~*"
	BinaryCast            BinaryOperator = "::"
	BinaryArrayOverlap    BinaryOperator = "&&"
	BinaryDistance        BinaryOperator = "<->"
	BinaryRangeAdjacent   BinaryOperator = "-|-"
	BinaryShiftLeft       BinaryOperator = "<<"
	BinaryShiftRight      BinaryOperator = ">>"
	BinaryContains        BinaryOperator = "@>"
	BinaryContainedBy     BinaryOperator = "<@"
	BinaryJsonHasKey      BinaryOperator = "?"
	BinaryJsonHasAnyKey   BinaryOperator = "?|"
	BinaryNotExtendsRight BinaryOperator = "&<"
	BinaryNotExtendsLeft  BinaryOperator = "&>"
	BinaryJsonHasAllKeys  BinaryOperator = "?&"
	BinaryQuestionColon   BinaryOperator = "?:"
	BinaryFullTextSearch  BinaryOperator = "@@"
)

// BinaryExpr is [BinaryOperator, left, right] — the non-foldable binary
// operators (query.ts : "Here are all binary for who folding makes little
// sense"), always exactly two operands.
type BinaryExpr struct {
	notYetValidated
	Op    BinaryOperator
	Left  Expression
	Right Expression
}

type FoldedOperator string

const (
	FoldAnd              FoldedOperator = "and"
	FoldOr               FoldedOperator = "or"
	FoldAdd              FoldedOperator = "+"
	FoldSub              FoldedOperator = "-"
	FoldMul              FoldedOperator = "*"
	FoldDiv              FoldedOperator = "/"
	FoldPow              FoldedOperator = "^"
	FoldMod              FoldedOperator = "%"
	FoldBitOr            FoldedOperator = "|"
	FoldBitAnd           FoldedOperator = "&"
	FoldJsonGet          FoldedOperator = "->"
	FoldJsonGetText      FoldedOperator = "->>"
	FoldJsonPathGet      FoldedOperator = "#>"
	FoldJsonPathGetText  FoldedOperator = "#>>"
	FoldDot              FoldedOperator = "."
	FoldConcat           FoldedOperator = "||"
	FoldConcatCoalescing FoldedOperator = "||?"
	FoldCoalesceAlias    FoldedOperator = "??"
	FoldLte              FoldedOperator = "<="
	FoldGte              FoldedOperator = ">="
	FoldLt               FoldedOperator = "<"
	FoldGt               FoldedOperator = ">"
	FoldEq               FoldedOperator = "="
	// FoldNeq is canonical for both "<>" and its JS-alias spelling "!=" —
	// ParseExpression normalizes the latter into this at parse time, so
	// nothing downstream has to know both spellings exist.
	FoldNeq FoldedOperator = "<>"
	// FoldIsDistinctFrom is canonical for both "is-distinct-from" and "!==".
	FoldIsDistinctFrom FoldedOperator = "is-distinct-from"
	// FoldIsNotDistinctFrom is canonical for both "is-not-distinct-from" and
	// "===".
	FoldIsNotDistinctFrom FoldedOperator = "is-not-distinct-from"
)

// FoldedExpr is the post-folding, always-exactly-two-operand form of a
// query.ts [FoldedOperator, ...Expression] node. See foldExpression in
// expression_parse.go for how an N-ary folded array collapses into nested
// FoldedExpr nodes at parse time — everything downstream only ever sees
// binary nodes here, never a variadic list.
type FoldedExpr struct {
	notYetValidated
	Op    FoldedOperator
	Left  Expression
	Right Expression
}

// ---- between / in / any-all ----------------------------------------------------

// BetweenExpr is ["between"|"not-between", min, exp, max].
type BetweenExpr struct {
	notYetValidated
	Negate        bool
	Min, Exp, Max Expression
}

// InCandidate is one candidate of an in/not-in list. A bare JSON string
// candidate means a literal string there — query.ts's explicit carve-out
// ("candidates literal strings are here treated as literal strings and not
// columns") — unlike every other Expression position, where a bare string
// means an identifier. Exactly one of Literal/Expr is set (IsLiteral tells
// which).
type InCandidate struct {
	IsLiteral bool
	Literal   string
	Expr      Expression
}

// InExpr is ["in"|"not-in", subject, ...candidates].
type InExpr struct {
	notYetValidated
	Negate     bool
	Subject    Expression
	Candidates []InCandidate
}

// AnyAllExpr is ["any"|"all", op, subject, array_or_list]. Op is kept as the
// raw operator string rather than typed BinaryOperator/FoldedOperator, since
// query.ts allows either vocabulary here (`op: FoldedOperator |
// BinaryOperator`) and the two are disjoint sets of Go constants ; membership
// is still checked at parse time.
type AnyAllExpr struct {
	notYetValidated
	All     bool // true for "all", false for "any"
	Op      string
	Subject Expression
	Array   Expression
}

// ---- misc functions -------------------------------------------------------------

// ConcatWsExpr is ["concat_ws", separator, ...Expression].
type ConcatWsExpr struct {
	notYetValidated
	Separator Expression
	Args      []Expression
}

// CoalesceExpr is ["coalesce", ...Expression].
type CoalesceExpr struct {
	notYetValidated
	Args []Expression
}

// FormatExpr is ["format", format, ...Expression].
type FormatExpr struct {
	notYetValidated
	Format string
	Args   []Expression
}

// FunctionRef names a function/aggregate for AggExpr/CallExpr. query.ts
// accepts either a bare string (Name only, Schema "") or an explicit
// {schema, name} object for this position — never a single "schema.name"
// string. A bare string is never split on "." to guess at a schema :
// Postgres allows a quoted identifier to itself contain a literal dot (e.g.
// `"my.schema".func`), so splitting would need a real quote-aware identifier
// parser — the inverse of EscapeId (writer/writer.go), which nothing here
// implements — to be correct in general. Schema == "" means unqualified :
// resolved against the configured search path at pass 2, never guessed from
// the string.
type FunctionRef struct {
	Schema string
	Name   string
}

// AggExpr is ["agg"|"aggregate", identifier, arguments, filter?]. Identifier
// is a FunctionRef, not an Expression, deliberately : query.ts's own note is
// that function/operator allowlisting has to be checkable at query-compile
// time against a static, schema-qualified name, which isn't possible if the
// identifier could itself be computed. Filter is nil if absent.
type AggExpr struct {
	notYetValidated
	Identifier FunctionRef
	Arguments  []Expression
	Filter     Expression
}

// CallExpr is ["call", identifier, ...arguments]. Same static-identifier
// constraint as AggExpr.
type CallExpr struct {
	notYetValidated
	Identifier FunctionRef
	Arguments  []Expression
}

// ---- object / select-shape family ------------------------------------------------

// ObjectExpr is an inline JSON object : { [name]: Expression }.
type ObjectExpr struct {
	notYetValidated
	Fields map[string]Expression
}

// OwnExpr is ["own"] : an object with all the columns of the current relation.
type OwnExpr struct{ notYetValidated }

// FullExpr is ["full"] : OwnExpr plus the joined relations. select's default
// when unspecified.
type FullExpr struct{ notYetValidated }

// OwnExceptExpr is ["own-except", except].
type OwnExceptExpr struct {
	notYetValidated
	Except []string
}

// FullExceptExpr is ["full-except", except].
type FullExceptExpr struct {
	notYetValidated
	Except []string
}

// OwnAndExpr is ["own-and", and].
type OwnAndExpr struct {
	notYetValidated
	And map[string]Expression
}

// FullAndExpr is ["full-and", and].
type FullAndExpr struct {
	notYetValidated
	And map[string]Expression
}

// OwnExceptAndExpr is ["own-except-and", except, and]. merge_with (And)
// cannot shadow keys implicitly per query.ts's comment ; that's a pass 2
// validation concern, not enforced by this shape.
type OwnExceptAndExpr struct {
	notYetValidated
	Except []string
	And    map[string]Expression
}

// FullExceptAndExpr is ["full-except-and", except, and]. Same shadowing note
// as OwnExceptAndExpr.
type FullExceptAndExpr struct {
	notYetValidated
	Except []string
	And    map[string]Expression
}

// ---- arrays / indexing -----------------------------------------------------------

// ArrExpr is ["arr"|"array", ...Expression].
type ArrExpr struct {
	notYetValidated
	Items []Expression
}

// LstExpr is ["lst"|"list", ...Expression].
type LstExpr struct {
	notYetValidated
	Items []Expression
}

// IndexExpr is ["index", array, index] — 1-indexed, like Postgres.
type IndexExpr struct {
	notYetValidated
	Array Expression
	Index Expression
}

// SliceExpr is ["slice", array, from, to] — 1-indexed, like Postgres.
type SliceExpr struct {
	notYetValidated
	Array    Expression
	From, To Expression
}

// ---- column granularity ----------------------------------------------------------

// GetSetExpr is ["get-set", column, default_get?, default_set?]. DefaultGet/
// DefaultSet are nil when absent, and may be DefaultKeyword.
type GetSetExpr struct {
	notYetValidated
	Column     string
	DefaultGet Expression
	DefaultSet Expression
}

// GetExpr is ["get", column, default_value?] : read-only granular field
// selection — this column will not be looked for / modified in write mode.
type GetExpr struct {
	notYetValidated
	Column       string
	DefaultValue Expression
}

// SetExpr is ["set", column, default_value?] : write-only granular field
// selection — not fetched in query mode, but expected in write mode.
type SetExpr struct {
	notYetValidated
	Column       string
	DefaultValue Expression
}

// ---- params -----------------------------------------------------------------------

// ParamExpr is ["$param", name, cast?] : for use with well-known queries.
// Cast is "" when absent.
type ParamExpr struct {
	notYetValidated
	Name string
	Cast string
}
