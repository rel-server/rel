// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pg

import (
	"fmt"

	"github.com/jackc/pgx/v5"
)

const (
	MODE_IN       = "i"
	MODE_OUT      = "o"
	MODE_INOUT    = "b"
	MODE_VARIADIC = "v"
	// MODE_TABLE is a RETURNS TABLE(...) column, distinct from a plain OUT parameter.
	MODE_TABLE = "t"
)

type FunctionArgument struct {
	Index int
	Name  string
	Type  *Type

	PgMode    string
	PgTypeOid int
}

func (f *FunctionArgument) IsIn() bool {
	return f.PgMode == MODE_IN
}

func (f *FunctionArgument) IsOut() bool {
	return f.PgMode == MODE_OUT
}

func (f *FunctionArgument) IsInOut() bool {
	return f.PgMode == MODE_INOUT
}

func (f *FunctionArgument) IsTableColumn() bool {
	return f.PgMode == MODE_TABLE
}

func (f *FunctionArgument) IsVariadic() bool {
	return f.PgMode == MODE_VARIADIC
}

//----------------------------------------------------------------------------------
//---------------------------- Function --------------------------------------------

type Function struct {
	Identifier SqlIdentifier

	// PgOid is pg_proc.oid, for EXECUTE privilege checks.
	PgOid int

	// The function's COMMENT ON, empty if unset.
	Comment string

	Arguments []FunctionArgument

	// PgNargs is the number of input arguments (OUT ones excluded, unlike len(Arguments)), PgNargsDefaults how
	// many trailing ones have a default.
	PgNargs         int
	PgNargsDefaults int

	// PgKind is prokind : "f" plain function, "p" procedure, "a" aggregate, "w" window function. Aggregates are
	// kept for "agg" expressions ; a relation root or "call" only takes "f".
	PgKind string

	ReturnsSet      bool // Whether this function returns a table() or a setof ReturnType
	ReturnType      *Type
	PgReturnTypeOid int

	// RecordRelation is a synthetic relation built from the OUT/TABLE arguments, since such a function returns a
	// plain record. Nil for any other function, including one returning SETOF a relation (see ReturnType.Relation).
	RecordRelation *Relation

	// Other function attributes that are not relevant as of now
	IsStrict            bool // proisstrict
	IsSetUid            bool
	IsVolatile          bool
	IsLeakproof         bool
	IsCalledOnNullInput bool
	IsImmutable         bool
	IsStable            bool
}

func (f *Function) ReturnsSingleRow() bool {
	return !f.ReturnsSet
}

// FirstInputArg returns f's first IN/INOUT/VARIADIC argument, the one a computed field binds the row to ;
// nil when there is none.
func (f *Function) FirstInputArg() *FunctionArgument {
	for i := range f.Arguments {
		a := &f.Arguments[i]
		if a.IsIn() || a.IsInOut() || a.IsVariadic() {
			return a
		}
	}
	return nil
}

func (f *Function) IsPlainFunction() bool  { return f.PgKind == "f" }
func (f *Function) IsAggregate() bool      { return f.PgKind == "a" }
func (f *Function) IsProcedure() bool      { return f.PgKind == "p" }
func (f *Function) IsWindowFunction() bool { return f.PgKind == "w" }

func (f *Function) String() string {
	return fmt.Sprintf("Function(%s())", f.Identifier.String())
}

// AcceptsArity reports whether a positional call with n arguments is valid : [PgNargs-PgNargsDefaults, PgNargs],
// widened by a VARIADIC argument to any count above and down to PgNargs-1.
func (f *Function) AcceptsArity(n int) bool {
	min := f.PgNargs - f.PgNargsDefaults
	variadic := false
	for i := range f.Arguments {
		if f.Arguments[i].IsVariadic() {
			variadic = true
			break
		}
	}
	if variadic && f.PgNargs-1 < min {
		min = f.PgNargs - 1
	}
	if n < min {
		return false
	}
	if n <= f.PgNargs {
		return true
	}
	return variadic
}

// IsExportable returns true if the function is exportable to the web.
// It can only be exported if all its arguments are IN, the return type is OUT and all arguments are named.
func (f *Function) IsExportable() bool {
	// return f.Arguments[0].Mode == "IN" && f.Arguments[len(f.Arguments)-1].Mode == "OUT"
	return false
}

// Query the database and fill the infos
func FillFunctionInformations(infos *DbInfos, conn *pgx.Conn) error {
	if err := scanIntoThroughJsonAgg(conn, INFO_QUERY_FUNCTIONS, &infos.Functions); err != nil {
		return err
	}

	// And then fill their elem/array counterparts

	return nil
}

var INFO_QUERY_FUNCTIONS = /* sql */ `
SELECT json_agg(S) FROM	(SELECT
  json_build_object(
    'Schema', n.nspname,
    'Name', p.proname
  ) AS "Identifier",
	obj_description(p.oid, 'pg_proc') AS "Comment",
	p.oid::integer AS "PgOid",
	p.prorettype::integer as "PgReturnTypeOid",
	p.pronargs::integer AS "PgNargs",
	p.pronargdefaults::integer AS "PgNargsDefaults",
	p.prokind::text AS "PgKind",
  l.lanname AS "Language",
  p.proretset AS "ReturnsSet",
  p.proisstrict AS "IsStrict",
  p.prosecdef AS "IsSetuid",
  p.provolatile = 'i' AS "IsImmutable",
  p.provolatile = 's' AS "IsStable",
  p.provolatile = 'v' AS "IsVolatile",
  (
    SELECT json_agg(S) FROM (
			SELECT
				argnb AS "Index",
				p.proargnames[argnb] AS "Name",
				coalesce(p.proargmodes[argnb], 'i') AS "PgMode",
				-- proallargtypes/proargmodes are NULL whenever every argument is a plain IN
				-- argument (the common case) ; fall back to proargtypes (always populated) then.
				-- proargtypes is an oidvector with 0-based subscripts, unlike a normal array,
				-- so it's renormalized to 1-based via unnest/array before indexing by argnb.
				(coalesce(p.proallargtypes, array(select unnest(p.proargtypes))))[argnb]::integer AS "PgTypeOid"
			FROM generate_series(1, array_length(coalesce(p.proallargtypes, array(select unnest(p.proargtypes))), 1)) argnb
		) S) AS "Arguments"
  FROM pg_proc p
  LEFT JOIN pg_namespace n ON p.pronamespace = n.oid
  LEFT JOIN pg_language l ON p.prolang = l.oid
  ORDER BY n.nspname, p.proname) S;
`
