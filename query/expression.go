package query

import "github.com/rel-server/rel/pg"

// Expression is a parsed node of query.ts's `Expression` grammar (pass 1) : purely syntactic, with no DB or
// scope lookup. Folded operators are already folded into nested binary FoldedExpr nodes.
type Expression interface {
	// Validate is a placeholder : every implementation is a no-op.
	//
	//> Question: pass 2 will likely need a scope argument here ; confirm the signature.
	Validate() error
}

// notYetValidated gives every node type below the no-op Validate.
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

// OrderByTerm is one order_by entry : a bare Expression (OrderAsc) or a [direction, Expression] pair.
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

// NumberLiteral is a bare JSON number ; values a float64 can't carry use BigIntLiteral/NumericLiteral.
type NumberLiteral struct {
	notYetValidated
	Value float64
}

// Identifier is a name reference to a column, alias or computed field of the current scope. A bare JSON string
// is never one (it is a StringLiteral) : it comes only from [".", name] and the later hop names of a "." chain.
// Resolved is nil until pass 2 sets it in place, hence always *Identifier.
type Identifier struct {
	notYetValidated
	Name     string
	Resolved ResolvedField
}

// StringLiteral is a bare JSON string : always a value, never a reference.
type StringLiteral struct {
	notYetValidated
	Value string
}

// BigIntLiteral is ["bigint", value].
type BigIntLiteral struct {
	notYetValidated
	Value string
}

// NumericLiteral is ["numeric", value].
type NumericLiteral struct {
	notYetValidated
	Value string
}

// DefaultKeyword is "default" in a col/get/set default-value position : the column's own DB default. Anywhere
// else "default" is an ordinary value.
//
// > Question: this reading of the original design note is unconfirmed.
type DefaultKeyword struct{ notYetValidated }

// ---- operators ----------------------------------------------------------------

type UnaryOperator string

const (
	UnaryNeg        UnaryOperator = "-"
	UnaryNot        UnaryOperator = "not"
	UnaryBitNot     UnaryOperator = "~"
	UnaryIsNull     UnaryOperator = "is_null"
	UnaryIsTrue     UnaryOperator = "is_true"
	UnaryIsFalse    UnaryOperator = "is_false"
	UnaryIsNotNull  UnaryOperator = "is_not_null"
	UnaryIsNotTrue  UnaryOperator = "is_not_true"
	UnaryIsNotFalse UnaryOperator = "is_not_false"
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

// BinaryExpr is [BinaryOperator, left, right] : the non-foldable operators, always two operands.
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
	// FoldNeq is canonical for both "<>" and "!=" ; the parser normalizes "!=".
	FoldNeq FoldedOperator = "<>"
	// FoldIsDistinctFrom is canonical for both "is_distinct_from" and "!==".
	FoldIsDistinctFrom FoldedOperator = "is_distinct_from"
	// FoldIsNotDistinctFrom is canonical for both "is_not_distinct_from" and "===".
	FoldIsNotDistinctFrom FoldedOperator = "is_not_distinct_from"
)

// FoldedExpr is one binary step of a folded [FoldedOperator, ...Expression] array, always two operands.
type FoldedExpr struct {
	notYetValidated
	Op    FoldedOperator
	Left  Expression
	Right Expression
}

// ---- between / in / any-all ----------------------------------------------------

// BetweenExpr is ["between"|"not_between", min, exp, max].
type BetweenExpr struct {
	notYetValidated
	Negate        bool
	Min, Exp, Max Expression
}

// InExpr is ["in"|"not_in", subject, ...candidates].
type InExpr struct {
	notYetValidated
	Negate     bool
	Subject    Expression
	Candidates []Expression
}

// AnyAllExpr is ["any"|"all", op, subject, array_or_list]. Op is a raw string since it may be either a
// FoldedOperator or a BinaryOperator ; the parser checks it is one.
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

// FunctionRef names a function/aggregate : a bare string (Schema "") or a {schema, name} object. A bare string is
// never split on ".", since a quoted name may contain a dot. Schema "" resolves against the search path at pass 2.
type FunctionRef struct {
	Schema string
	Name   string
}

// AggExpr is ["agg"|"aggregate", identifier, arguments, options?]. Identifier is a static FunctionRef so the
// function blacklist can be checked at compile time. ResolvedFunction, an aggregate, is set in place by pass 2.
type AggExpr struct {
	notYetValidated
	Identifier FunctionRef
	Arguments  []Expression
	// Filter (nil if absent), Distinct and OrderBy come from the options object {filter?, distinct?, order_by?}.
	Filter           Expression
	Distinct         bool
	OrderBy          []OrderByTerm
	ResolvedFunction *pg.Function
}

// CallExpr is ["call", identifier, ...arguments], like AggExpr but ResolvedFunction is a plain function.
type CallExpr struct {
	notYetValidated
	Identifier       FunctionRef
	Arguments        []Expression
	ResolvedFunction *pg.Function
}

// ---- object / select-shape family ------------------------------------------------

// ObjectExpr is an inline JSON object : { [name]: Expression }.
type ObjectExpr struct {
	notYetValidated
	Fields map[string]Expression
}

// StarExpr is ["*", ...] (own columns plus joined aliases, select's default) or ["*~", ...] (Own : own columns
// only). Each trailing argument is an except-list or an and-map, in any number and order : except-lists are
// concatenated, and-maps merged with later keys winning.
type StarExpr struct {
	notYetValidated
	Own    bool
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

// ColExpr is ["col", column, default_get?, default_set?]. Column must resolve to a physical column of the current
// relation, never an alias, embed or computed field, since a read/write default only makes sense there.
// DefaultGet/DefaultSet are nil when absent and may be DefaultKeyword. Pass 2 sets Resolved/ResolvedColumn in place.
type ColExpr struct {
	notYetValidated
	Column         string
	DefaultGet     Expression
	DefaultSet     Expression
	Resolved       ResolvedField
	ResolvedColumn *pg.Column
}

// GetExpr is ["get", column, default_value?] : read-only, ignored in write mode. Same column rule as ColExpr.
type GetExpr struct {
	notYetValidated
	Column         string
	DefaultValue   Expression
	ResolvedColumn *pg.Column
}

// SetExpr is ["set", column, default_value?] : write-only, not fetched on read. Same column rule as ColExpr.
type SetExpr struct {
	notYetValidated
	Column         string
	DefaultValue   Expression
	ResolvedColumn *pg.Column
}

// ---- params -----------------------------------------------------------------------

// ParamExpr is ["$param", name, cast?] in a well-known query ; Cast is "" when absent.
type ParamExpr struct {
	notYetValidated
	Name string
	Cast string
}
