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

	// The relation's own COMMENT ON, if any — meant primarily for the
	// TypeScript export to surface as a doc comment ; empty string if unset.
	Comment string

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

	obj_description(pg_class.oid, 'pg_class') AS "Comment",

	json_agg(json_build_object(
		'Name', column_name,
		'Index', ordinal_position,
		'Comment', col_description(pg_class.oid, ordinal_position),
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
-- Deliberately unfiltered : introspection needs the complete picture of the
-- database, pg_catalog/information_schema included (e.g. a function returning
-- a pg_catalog composite type still needs that type resolved). Restricting
-- pg_catalog/information_schema as *query targets* is a compile-time concern
-- (querying.md ### Scoping's relation blacklist), not an introspection-time one.

GROUP BY
pg_class.oid, pg_class.relnamespace, pg_class.relname

UNION ALL

-- information_schema.columns' own view definition filters to
-- relkind IN ('r','v','m','f','p') — a bare CREATE TYPE ... AS (...)
-- composite type (relkind 'c') never shows up there at all, so it's picked
-- up here instead, straight from pg_attribute. No overlap with the branch
-- above : a table's own row type lives on the table's pg_class row
-- (relkind 'r'), never as a separate 'c' entry, so relkind = 'c' can only
-- ever match a real standalone composite type.
SELECT
	pg_class.oid::integer AS "PgRelId",
	json_build_object(
		'Schema', pg_class.relnamespace::regnamespace,
		'Name', pg_class.relname
	) AS "Identifier",
	obj_description(pg_class.oid, 'pg_class') AS "Comment",
	json_agg(json_build_object(
		'Name', a.attname,
		'Index', a.attnum,
		'Comment', col_description(pg_class.oid, a.attnum),
		'DefaultExpression', NULL,
		'IsNullable', true,
		'IsSelfReferencing', false,
		'IsIdentity', false,
		'IsUpdatable', false,
		'PgTypeOid', a.atttypid::integer,
		'DomainIdentifier', NULL
	) ORDER BY a.attnum) AS "Columns"
FROM pg_class
JOIN pg_attribute a ON a.attrelid = pg_class.oid AND a.attnum > 0 AND NOT a.attisdropped
WHERE pg_class.relkind = 'c'
GROUP BY pg_class.oid, pg_class.relnamespace, pg_class.relname
) R;`
