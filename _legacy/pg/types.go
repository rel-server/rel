package pg

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"sort"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Function struct {
	IsExported       bool                `json:"is_exported"`
	Schema           string              `json:"schema"`
	Name             string              `json:"name"`
	Language         string              `json:"language"`
	ReturnSchema     string              `json:"return_schema"`
	ReturnsSet       bool                `json:"returns_set"`
	ReturnType       string              `json:"return_type"`
	IsStrict         bool                `json:"is_strict"`
	IsSetuid         bool                `json:"is_setuid"`
	Volatility       string              `json:"volatility"`
	IsLeakproof      bool                `json:"is_leakproof"`
	IsImmutable      bool                `json:"is_immutable"`
	ReturnsComposite bool                `json:"returns_composite"`
	Arguments        []*FunctionArgument `json:"arguments"`
}

func (f *Function) DbTableName() string {
	return f.Schema + "." + f.Name
}

type FunctionArgument struct {
	Index  int    `json:"index"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Schema string `json:"schema"`
	Mode   string `json:"mode"`
}

type DBColumn struct {
	// Index          int     `json:"index"`
	Table *DBTable `json:"-"`

	Name            string  `json:"name"`
	IsExported      bool    `json:"is_exported"`
	TypeSchema      string  `json:"type_schema"`
	Type            string  `json:"type"`
	Default         *string `json:"default"`
	NotNull         bool    `json:"is_notnull"`
	Pk              bool    `json:"is_pk"`
	Hidden          bool    `json:"is_hidden"`        // SQLite only
	NotShown        bool    `json:"not_shown"`        // prefixed with _
	ReallyNotShown  bool    `json:"really_not_shown"` // prefixed with __
	IsCompositeType bool    `json:"is_composite"`     // Whether the targeted column is a composite type
}

func (c *DBColumn) TypeName() string {
	return "\"" + c.TypeSchema + "\".\"" + c.Type + "\""
}

type ComputedColumn struct {
	Table *DBTable `json:"-"`

	Schema     string `json:"schema"`
	IsExported bool   `json:"is_exported"`
	Name       string `json:"name"`
	Type       string `json:"type"`
	TypeSchema string `json:"type_schema"`
}

type DBColumnConstraint struct {
	// The name of the constraint
	Name string `json:"name"`

	// The kind of constraint
	Kind string `json:"kind"`

	// The columns implicated by the constraint
	ColumnsNames []string `json:"columns"`

	// The target table of the constraint. If Pk or unique, this is the table itself
	TargetSchema  string   `json:"target_schema"`
	TargetName    string   `json:"target_name"`
	TargetColumns []string `json:"target_columns"`
}

func (c *DBColumnConstraint) IsPk() bool {
	return c.Kind == "PRIMARY KEY"
}

func (c *DBColumnConstraint) IsUnique() bool {
	return c.Kind == "UNIQUE"
}

func (c *DBColumnConstraint) IsForeignKey() bool {
	return c.Kind == "FOREIGN KEY"
}

type RelationShip struct {
	LocalRelation *DBTable `json:"-"`

	Columns      []*DBColumn `json:"-"`
	ColumnsNames []string    `json:"columns"`

	DistantRelation     *DBTable    `json:"-"`
	DistantColumns      []*DBColumn `json:"-"`
	DistantColumnsNames []string    `json:"distant_columns"`

	ConstraintName string `json:"constraint"`
	// Whether the relation is from the distant relation to the local one
	IsReverse bool `json:"reverse"`

	// If not reverse, and only if my columns are all not null
	IsNotNull bool `json:"is_notnull"`

	// Whether this relation is expected to produce multiple rows instead of a single one
	IsMultiple bool `json:"is_multiple"`
}

type DBTable struct {
	Schema     string `json:"schema"`
	Name       string `json:"name"`
	IsExported bool   `json:"is_exported"`

	IsRealTable bool `json:"is_real_table"`

	Columns        DBAllColumns      `json:"-"`
	ColumnsInOrder DBAllColumnsSlice `json:"columns"`

	ComputedColumns    []*ComputedColumn          `json:"computed_columns"`
	ComputedColumnsMap map[string]*ComputedColumn `json:"-"`

	UniqueColumnGroups map[string]bool                `json:"unique_columns"`
	Constraints        map[string]*DBColumnConstraint `json:"constraints"`
	Pk                 []*DBColumn

	// Foreign keys to other relations
	Fks map[string]*RelationShip `json:"fks"`

	// Incoming foreign keys from other relations.
	Incoming map[string]*RelationShip `json:"incoming"`

	// Relationships are the relationships between this table and others
	Relationships    []*RelationShip `json:"relationships"`
	RelationshipsMap RelationShipMap `json:"-"`
}

func (t *DBTable) AllColumnsNames() iter.Seq2[int, string] {
	return func(yield func(int, string) bool) {
		for i, col := range t.ColumnsInOrder {
			if !yield(i, col.Name) {
				return
			}
		}
	}
}

type RelationShipMap map[string]*RelationShip

func SnakeCaseClassName(name string) string {
	var s = name
	var result strings.Builder
	capitalizeNext := true

	for _, char := range s {
		if char == '_' {
			capitalizeNext = true
			continue
		}
		if capitalizeNext {
			result.WriteRune(unicode.ToUpper(char))
			capitalizeNext = false
		} else {
			result.WriteRune(char)
		}
	}

	return result.String()
}

type DBAllColumns map[string]*DBColumn
type DBAllColumnsSlice []*DBColumn
type DBAllTables map[string]*DBTable
type DBFunctionMap map[string]*Function

func (all DBAllTables) InOrder() []*DBTable {
	var result = make([]*DBTable, 0)
	for _, table := range all {
		if table.IsExported {
			result = append(result, table)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name < result[j].Name
	})
	return result
}

////////////////////////////////////////////

func (t *DBTable) DbTableName() string {
	return t.Schema + `.` + t.Name
}

func ReloadTables(_pool *pgxpool.Pool, schemas []string) (DBAllTables, map[string]*Function, error) {

	pool, err := _pool.Acquire(context.Background())
	if err != nil {
		return nil, nil, fmt.Errorf("pg schema, acquiring pool %w", err)
	}
	defer pool.Release()

	rows, err := pool.Query(context.Background() /* sql */, `
	WITH
	composite_types AS (
  SELECT
    n.nspname AS type_schema,
    t.typname AS type_name,
    a.attname AS column_name,
    a.attnum AS ordinal_position,
    tn.nspname AS udt_schema,
    bt.typname AS udt_name,
    bt.typtype = 'c' as is_composite,
    a.attnotnull AS is_notnull
  FROM pg_type t
  JOIN pg_class c ON c.oid = t.typrelid
  JOIN pg_namespace n ON n.oid = t.typnamespace
  JOIN pg_attribute a ON a.attrelid = c.oid
  JOIN pg_type bt ON bt.oid = a.atttypid
  JOIN pg_namespace tn ON tn.oid = bt.typnamespace
  WHERE
    c.relkind = 'c'
    AND a.attnum > 0
    AND NOT a.attisdropped
),

  pks as (
    SELECT
	c.table_schema, c.table_name, c.column_name
FROM information_schema.table_constraints tc
JOIN information_schema.constraint_column_usage AS ccu USING (constraint_schema, constraint_name)
JOIN information_schema.columns AS c ON c.table_schema = tc.constraint_schema
  AND tc.table_name = c.table_name AND ccu.column_name = c.column_name
WHERE constraint_type = 'PRIMARY KEY' -- and tc.table_name = 'mytable';
  and tc.constraint_schema not in ('information_schema', 'pg_catalog')
  ),

_constraints AS (
	SELECT
			tc.constraint_name as name,
			tc.constraint_type as kind,
			tc.table_schema,
			tc.table_name,
			cols.columns,
			dst.table_schema AS target_schema,
			dst.table_name as target_name,
			dst.columns AS target_columns

	FROM information_schema.table_constraints AS tc
	JOIN LATERAL (SELECT json_agg(column_name order by column_name) as columns FROM information_schema.key_column_usage AS kcu WHERE tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema) cols ON TRUE
	JOIN LATERAL (
		SELECT ccu.table_schema, ccu.table_name, json_agg(ccu.column_name order by ccu.column_name) as columns
		FROM information_schema.constraint_column_usage AS ccu
		WHERE ccu.constraint_name = tc.constraint_name
		GROUP BY ccu.table_schema, ccu.table_name
	) dst ON true
),
_table_constraints AS (
	SELECT table_schema, table_name, json_object_agg(name, json_build_object(
		'name', name,
		'kind', kind,
		'columns', COLUMNS,
		'target_schema', target_schema,
		'target_name', target_name,
		'target_columns', target_columns
		)
	) as constraints from _constraints
	GROUP BY table_schema, table_name
)

SELECT res FROM (select
	json_build_object(
			'schema', cols.table_schema,
			'name', cols.table_name,
			'is_real_table', true,
			'columns', json_agg(
				json_build_object(
					'name', cols.column_name,
					'type_schema', cols.udt_schema,
					'type', cols.udt_name,
					'is_notnull', cols.is_nullable = 'NO',
					'is_pk', pks.column_name is not null,
					'default', case when cols.is_identity = 'YES' then 'nextval(pg_get_serial_sequence(''' || cols.table_schema || '.' || cols.table_name || ''', ''' || cols.column_name || '''))' else cols.column_default end,
					'is_composite', (
						SELECT cols_type.typtype = 'c'
						FROM pg_type cols_type
							INNER JOIN pg_namespace nms
							  ON nms.oid = cols_type.typnamespace
							  AND nms.nspname = cols.udt_schema
							WHERE cols_type.typname = cols.udt_name
					)
				) order by cols.ordinal_position
			),
			'pkcolumns', json_agg(pks.column_name order by cols.ordinal_position) filter (where pks.column_name is not null),
			'constraints', (select constraints from _table_constraints where table_schema = cols.table_schema and table_name = cols.table_name)
    ) as res

from information_schema.columns cols
left join pks
	on pks.table_schema = cols.table_schema
    and pks.table_name = cols.table_name
    and pks.column_name = cols.column_name
group by cols.table_schema, cols.table_name
order by cols.table_schema,
         cols.table_name) Q

UNION ALL

SELECT res FROM (SELECT
  json_build_object(
    'schema', type_schema,
    'name', type_name,
		'is_real_table', false,
    'columns', json_agg(
      json_build_object(
        'name', column_name,
        'type_schema', udt_schema,
        'type', udt_name,
        'is_notnull', is_notnull,
        'is_pk', false,
				'is_composite', is_composite,
        'default', null
      ) ORDER BY ordinal_position
    ),
    'pkcolumns', NULL,
    'constraints', NULL
  ) AS res
FROM composite_types
GROUP BY type_schema, type_name) Q

	`)
	if err != nil {
		return nil, nil, fmt.Errorf("when reading pg schema: %w", err)
	}
	defer rows.Close()

	var tables = make(DBAllTables)

	for rows.Next() {
		var jsonstr string
		err := rows.Scan(&jsonstr)
		if err != nil {
			return nil, nil, fmt.Errorf("scanning pg row: %w", err)
		}

		var table DBTable
		if err := json.Unmarshal([]byte(jsonstr), &table); err != nil {
			return nil, nil, fmt.Errorf("can't unmarshal dbtable: %w", err)
		}

		table.Columns = make(DBAllColumns)
		for _, c := range table.ColumnsInOrder {
			table.Columns[c.Name] = c
		}

		table.Relationships = make([]*RelationShip, 0)
		table.RelationshipsMap = make(map[string]*RelationShip)
		table.ComputedColumnsMap = make(map[string]*ComputedColumn)
		table.ComputedColumns = make([]*ComputedColumn, 0)

		// tables[table.Name] = &table
		tables[table.DbTableName()] = &table

		// srv.LogInfo("PG found table ", table.Name)
	}

	// Initialization
	for _, table := range tables {
		if table.UniqueColumnGroups == nil {
			table.UniqueColumnGroups = make(map[string]bool)
		}

		// test that the schema of the table is in srv.Schemas
		if slices.Contains(schemas, table.Schema) {
			table.IsExported = true
		}

		for _, column := range table.ColumnsInOrder {
			column.Table = table

			if slices.Contains(schemas, column.TypeSchema) || column.TypeSchema == "pg_catalog" {
				column.IsExported = true
			}
		}

		for _, constraint := range table.Constraints {
			columns_str := strings.Join(constraint.ColumnsNames, ",")

			if constraint.IsPk() {
				table.Pk = make([]*DBColumn, len(constraint.ColumnsNames))
				for i, col := range constraint.ColumnsNames {
					table.Pk[i] = table.Columns[col]
				}
				table.UniqueColumnGroups[columns_str] = true
			}
			if constraint.IsUnique() {
				table.UniqueColumnGroups[columns_str] = true
			}
		}
	}

	tables.BuildRelationships(schemas)

	rows, err = pool.Query(context.Background() /* sql */, `
		SELECT row_to_json(S) FROM	(SELECT n.nspname AS schema,
					p.proname AS name,
					l.lanname AS language,
					p.proretset AS returns_set,
					t.typname AS return_type,
					n2.nspname as return_schema,
					p.proisstrict as is_strict,
					p.prosecdef as is_setuid,
					t.typtype = 'c' as returns_composite,
					case WHEN p.provolatile = 'i' then 'immutable'
						WHEN p.provolatile = 's' THEN 'stable'
						else 'volatile'
					END as volatility,
					(select array_agg(S) FROM (SELECT
						argnb as index,
						p.proargnames[argnb] as name,
						t.typname as type,
						n.nspname as schema,
						p.proargmodes[argnb] as mode
					FROM generate_series(1, p.pronargs) argnb
						left join pg_type t ON t.oid = p.proargtypes[argnb-1] -- 0 based ?
						left join pg_namespace n ON n.oid = t.typnamespace -- 0 based ?
					) S) as arguments
		FROM pg_proc p
		LEFT JOIN pg_namespace n ON p.pronamespace = n.oid
		LEFT JOIN pg_language l ON p.prolang = l.oid
		LEFT JOIN pg_type t ON t.oid = p.prorettype
		LEFT JOIN pg_namespace n2 ON n2.oid = t.typnamespace
		ORDER BY schema, name) S;
	`)
	if err != nil {
		return nil, nil, fmt.Errorf("when reading pg functions: %w", err)
	}
	defer rows.Close()

	var functions = make(map[string]*Function)

	for rows.Next() {
		var jsonstr string

		err := rows.Scan(&jsonstr)
		if err != nil {
			return nil, nil, fmt.Errorf("scanning pg row: %w", err)
		}

		var function Function
		if err := json.Unmarshal([]byte(jsonstr), &function); err != nil {
			return nil, nil, fmt.Errorf("can't unmarshal function: %w", err)
		}
		function.IsExported = slices.Contains(schemas, function.Schema)

		if len(function.Arguments) == 1 && !function.ReturnsSet {
			arg := function.Arguments[0]
			arg_full_type_name := arg.Schema + "." + arg.Type

			// pp.Println(arg_full_type_name, arg.Name, arg.Schema, arg.Type)
			if tbl, ok := tables[arg_full_type_name]; ok {
				var computed_column = &ComputedColumn{
					Table:      tbl,
					Schema:     function.Schema,
					Name:       function.Name,
					IsExported: slices.Contains(schemas, function.Schema),
					Type:       function.ReturnType,
					TypeSchema: function.ReturnSchema,
				}
				tbl.ComputedColumns = append(tbl.ComputedColumns, computed_column)
				tbl.ComputedColumnsMap[computed_column.Name] = computed_column
			}
		}

		functions[function.DbTableName()] = &function
	}

	// srv.LogInfo("loaded pg tables")
	// Tables = tables
	return tables, functions, nil
}
