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

func (f *FunctionArgument) IsVariadic() bool {
	return f.PgMode == MODE_VARIADIC
}

//----------------------------------------------------------------------------------
//---------------------------- Function --------------------------------------------

type Function struct {
	Identifier SqlIdentifier

	Arguments []FunctionArgument

	ReturnsSet      bool // Whether this function returns a table() or a setof ReturnType
	ReturnType      *Type
	PgReturnTypeOid int

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

func (f *Function) String() string {
	return fmt.Sprintf("Function(%s())", f.Identifier.String())
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
	p.prorettype::integer as "PgReturnTypeOid",
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
				p.proargmodes[argnb] AS "PgMode",
				p.proallargtypes[argnb]::integer AS "PgTypeOid"
			FROM generate_series(1, array_length(proallargtypes, 1)) argnb
		) S) AS "Arguments"
  FROM pg_proc p
  LEFT JOIN pg_namespace n ON p.pronamespace = n.oid
  LEFT JOIN pg_language l ON p.prolang = l.oid
  ORDER BY n.nspname, p.proname) S;
`
