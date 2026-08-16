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
	"github.com/jackc/pgx/v5"
)

// An index, distinct from (though sometimes backing) a Constraint : a table's
// index inventory and its constraint inventory are related but separate facts.
type Index struct {
	Name     string
	RelId    int
	IsUnique bool

	// True declared key-column order, INCLUDE-only columns already excluded
	// (see the introspection query : sliced to indnkeyatts). A join is only
	// ever checked against a leading prefix of these, never the tail.
	Columns []string
}

func FillIndexInformations(infos *DbInfos, conn *pgx.Conn) error {
	var raw []Index
	if err := scanIntoThroughJsonAgg(conn, INFO_QUERY_INDEXES, &raw); err != nil {
		return err
	}

	for i := range raw {
		idx := raw[i]
		relation := infos.GetRelation(idx.RelId)
		if relation == nil {
			continue // index on a relation outside information_schema.columns's relkinds (e.g. a TOAST table) ; irrelevant, skip
		}
		relation.Indexes = append(relation.Indexes, &idx)
		relation.registerIndexCoverage(idx.Columns)
	}

	return nil
}

// registerIndexCoverage records every leading prefix of columns (in true
// index order) as a valid equality-lookup key — an index on (a,b,c) can serve
// a lookup on {a}, {a,b}, or {a,b,c}, not just its full column set.
func (r *Relation) registerIndexCoverage(columns []string) {
	if r.indexCoverage == nil {
		r.indexCoverage = make(map[string]struct{})
	}
	for i := 1; i <= len(columns); i++ {
		r.indexCoverage[sortedColumnKey(columns[:i])] = struct{}{}
	}
}

// IsIndexed reports whether columns (as a set) form a usable leading prefix of
// some index's key columns on r — see querying.md ### Scoping, "Join
// eligibility : indexing, not just correctness".
func (r *Relation) IsIndexed(columns []string) bool {
	_, ok := r.indexCoverage[sortedColumnKey(columns)]
	return ok
}

var INFO_QUERY_INDEXES = /* sql */ `
SELECT json_agg(I) FROM (SELECT
	cl.relname AS "Name",
	i.indrelid::integer AS "RelId",
	i.indisunique AS "IsUnique",
	(
		SELECT array_agg(a.attname ORDER BY k.ord)
		FROM unnest(i.indkey[0:i.indnkeyatts - 1]) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
	) AS "Columns"
-- Deliberately unfiltered by schema — see info_relation.go's INFO_QUERY_RELATIONS
-- comment : introspection needs the complete picture, pg_catalog included.
FROM pg_index i
JOIN pg_class cl ON cl.oid = i.indexrelid
WHERE i.indisready AND i.indisvalid
	-- a partial index only guarantees coverage for rows matching its predicate,
	-- which rel has no way to verify subsumes a query's actual row set
	AND i.indpred IS NULL
	-- an expression index's indkey carries a 0 at the expression's position ;
	-- on only ever joins on plain columns, never on an expression's result
	AND i.indexprs IS NULL
) I;`
