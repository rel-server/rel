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

// A table or view
type Relation struct {
	Identifier SqlIdentifier

	IsView             bool
	IsMaterializedView bool

	Columns    []*Column
	ColumnsMap map[string]*Column

	Type *Type // The related type

	PrimaryKey          *Constraint
	IncomingForeignKeys []*Constraint
	OutgoingForeignKeys []*Constraint

	// Indexes is kept even though only IsIndexed below consumes it today —
	// it's a distinct capability (query-plan safety) from constraints (data
	// integrity), and useful on its own for future diagnostics/tooling.
	Indexes []*Index

	// Unexported : callers go through the lookup API (FindConstraintByName,
	// FindUniqueConstraint, RelationshipsTo, ResolveJoin, IsIndexed) instead of
	// building canonical keys themselves. See info_constraint.go / info_index.go.
	byName             map[string]*Constraint
	uniqueColumnGroups map[string]*Constraint
	byOtherRelation    map[int][]*Constraint
	indexCoverage      map[string]struct{}

	PgRelId   int
	PgTypeOid int
}

func FillRelationInformations(infos *DbInfos, conn *pgx.Conn) error {
	if err := scanIntoThroughJsonAgg(conn, INFO_QUERY_RELATIONS, &infos.Relations); err != nil {
		return err
	}

	for _, relation := range infos.Relations {
		relation.ColumnsMap = make(map[string]*Column)
		relation.byName = make(map[string]*Constraint)
		relation.uniqueColumnGroups = make(map[string]*Constraint)
		relation.byOtherRelation = make(map[int][]*Constraint)

		infos.RelationMapByRelid[relation.PgRelId] = relation
		for _, column := range relation.Columns {
			relation.ColumnsMap[column.Name] = column
		}
	}

	return nil
}

var INFO_QUERY_RELATIONS = /* sql */ `
SELECT json_agg(R) FROM (SELECT

	pg_class.oid::integer AS "PgRelId",

	json_build_object(
		'Schema', pg_class.relnamespace::regnamespace,
		'Name', pg_class.relname
	) AS "Identifier",

	json_agg(json_build_object(
		'Name', column_name,
		'Index', ordinal_position,
		'DefaultExpression', CASE WHEN is_identity = 'YES' AND pg_get_serial_sequence(col.table_schema || '.' || col.table_name, col.column_name) IS NOT NULL
			THEN 'nextval(''' || pg_get_serial_sequence(col.table_schema || '.' || col.table_name, col.column_name) || ''')'
			ELSE column_default
		END,
		'IsNullable', is_nullable = 'YES',
		'IsSelfReferencing', is_self_referencing = 'YES',
		'IsIdentity', is_identity = 'YES',
		'IsUpdatable', is_updatable = 'YES',
		'PgTypeOid', (SELECT t.oid::INT FROM pg_type t WHERE t.typname = udt_name AND t.typnamespace = udt_schema::regnamespace),
		'DomainIdentifier', CASE WHEN domain_schema IS NULL THEN NULL ELSE json_build_object(
				'Schema', domain_schema,
				'Name', domain_name
			) END
		) ORDER BY ordinal_position
	) AS "Columns"

FROM information_schema.columns col
INNER JOIN pg_class ON pg_class.relname = col.table_name AND pg_class.relnamespace = col.table_schema::regnamespace
-- system catalogs are never valid query targets (see querying.md ### Scoping) ;
-- excluding them here avoids introspecting thousands of irrelevant relations.
WHERE col.table_schema NOT IN ('pg_catalog', 'information_schema')

GROUP BY
pg_class.oid, pg_class.relnamespace, pg_class.relname
) R;`
