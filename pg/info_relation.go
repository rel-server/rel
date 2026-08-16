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

	AllConstraints []*DBConstraint // all constraints coming from the database before resolving them

	PrimaryKey          *Constraint
	UniqueColumnGroups  map[string]*Constraint
	IncomingForeignKeys []*Constraint
	OutgoingForeignKeys []*Constraint
	Indexes             []*Constraint

	/** Map of constraints by name where column names are stored in alphabetical order. Will also map constraints names with and without schema prefix. There is a (low) risk of name collisions for tables referencing each other from different schemas but similar names where auto-generated constraints names are the same. */
	ConstraintsByName    map[string]*Constraint
	AmbiguousConstraints map[string][]*Constraint

	PgRelId   int
	PgTypeOid int
}

func (r *Relation) addAmbiguousConstraint(name string, c *Constraint) {
	if _, ok := r.AmbiguousConstraints[name]; ok {
		r.AmbiguousConstraints[name] = append(r.AmbiguousConstraints[name], c)
		return
	} else {
		r.AmbiguousConstraints[name] = []*Constraint{c}
	}
}

// AddConstraint adds a constraint to the relation and sets up the appropriate names. If there is already a constraint with the same name, an ambiguity will be stored to create the relevant errors for the user.
func (r *Relation) AddConstraintByName(name string, c *Constraint) {

	if _, ok := r.AmbiguousConstraints[name]; ok {
		r.addAmbiguousConstraint(name, c)
		return
	}

	if _, ok := r.ConstraintsByName[name]; ok {
		r.addAmbiguousConstraint(name, c)
		r.ConstraintsByName[name] = nil
	}

	r.ConstraintsByName[name] = c
}

func FillRelationInformations(infos *DbInfos, conn *pgx.Conn) error {
	if err := scanIntoThroughJsonAgg(conn, INFO_QUERY_RELATIONS, &infos.Relations); err != nil {
		return err
	}

	// Build the map because we're gonna use it a lot when processing constraints.
	for _, relation := range infos.Relations {
		relation.ColumnsMap = make(map[string]*Column)
		relation.ConstraintsByName = make(map[string]*Constraint)
		relation.AmbiguousConstraints = make(map[string][]*Constraint)
		relation.UniqueColumnGroups = make(map[string]*Constraint)

		infos.RelationMapByRelid[relation.PgRelId] = relation
		for _, column := range relation.Columns {
			relation.ColumnsMap[column.Name] = column
		}
	}

	if err := processConstraints(infos); err != nil {
		return err
	}

	return nil
}

var INFO_QUERY_RELATIONS = /* sql */ `

WITH constraints AS (
	SELECT
    tc.constraint_type as "Type",
    tc.constraint_name as "Name",
		tc.table_schema,
		tc.table_name,

    "source"."SourceColumns" as "Columns",
		"source"."SourceColumnsSortedName" as "ColumnsSortedName",
    -- For foreign keys: referenced table and columns
		"target"."RelId" as "TargetRelId",
    "target"."TargetColumns",
		"target"."TargetColumnsSortedName" as "TargetColumnsSortedName"
	FROM information_schema.table_constraints tc
	JOIN LATERAL (
		SELECT
		  json_agg(kcu.column_name::TEXT ORDER BY kcu.ordinal_position) AS "SourceColumns",
			string_agg(kcu.column_name::TEXT, ',' ORDER BY kcu.column_name) AS "SourceColumnsSortedName"

		FROM information_schema.key_column_usage kcu
		WHERE kcu.constraint_name = tc.constraint_name AND tc.table_schema = kcu.table_schema
	) AS "source" ON TRUE
	LEFT JOIN LATERAL (
		SELECT
			cl.oid::integer AS "RelId",
			json_agg(ccu.column_name::text order by ccu.column_name) AS "TargetColumns",
			string_agg(ccu.column_name::text, ','  order by ccu.column_name) AS "TargetColumnsSortedName"
		FROM information_schema.constraint_column_usage ccu
		INNER JOIN pg_class cl ON ccu.table_schema::text = cl.relnamespace::regnamespace::text AND ccu.table_name = cl.relname
		WHERE ccu.constraint_name = tc.constraint_name AND tc.constraint_schema = ccu.constraint_schema
		GROUP BY cl.oid
	) AS "target" ON TRUE
	WHERE tc.constraint_type IN ('FOREIGN KEY', 'PRIMARY KEY', 'UNIQUE')
	ORDER BY tc.table_schema, tc.table_name, tc.constraint_type, tc.constraint_name
)

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
	) AS "Columns",

  (SELECT coalesce(json_agg(c), '[]'::json) FROM constraints c WHERE c.table_schema::regnamespace = pg_class.relnamespace AND c.table_name = pg_class.relname) AS "AllConstraints"

FROM information_schema.columns col
INNER JOIN pg_class ON pg_class.relname = col.table_name AND pg_class.relnamespace = col.table_schema::regnamespace

GROUP BY
pg_class.oid, pg_class.relnamespace, pg_class.relname
) R;`
