/*
This file contains the code to handle the relationships between tables.
*/
package pg

import (
	"slices"
	"sort"
	"strings"
)

/*
			BuildRelationships builds the relationships between tables.

			They get names to be used in the query language. These names try to be unique and memorable as much as possible. It is however not necessarily possible.

		  A relationship may be reached by several names. In general, the most certain way to name it is to use the foreign key name since it is guaranteed to be unique.

			Thus the following naming rules. When generating typescript code, the names are used as keys in the relationships map in the order of the following rules ;

			1. If the distant table name is unique across all relationships with this table, then the name of the relationship is the name of the distant table. An s is added _or discarded_ whether the relationship will net multiple elements or not.

	    2. If the distant table has only one relation to this one, then its qualified name is used.

			3. For relationships _to_ another table, the name is the column names joined by two underscores if more than one column participates

			4. The name of the constraint that defines it.
*/
func (tables DBAllTables) BuildRelationships(schemas []string) {

	// Once we have all the tables, we can build the relationships
	for _, table := range tables {

		for _, constraint := range table.Constraints {

			if !constraint.IsForeignKey() {
				continue
			}

			// If the distant table is not in the exposed schemas, we skip it
			distant_table := tables[constraint.TargetSchema+"."+constraint.TargetName]
			if distant_table == nil || !slices.Contains(schemas, distant_table.Schema) || !slices.Contains(schemas, table.Schema) {
				continue
			}

			local_columns := make([]*DBColumn, 0)
			local_columns_names := make([]string, 0)
			for _, col := range constraint.ColumnsNames {
				local_columns = append(local_columns, table.Columns[col])
				local_columns_names = append(local_columns_names, col)
			}

			distant_columns := make([]*DBColumn, 0)
			distant_columns_names := make([]string, 0)
			for _, col := range constraint.TargetColumns {
				distant_columns = append(distant_columns, distant_table.Columns[col])
				distant_columns_names = append(distant_columns_names, col)
			}

			sort.Strings(local_columns_names)
			sort.Strings(distant_columns_names)

			columns_str := strings.Join(constraint.ColumnsNames, ",")

			source_is_unique := table.UniqueColumnGroups[columns_str]

			rel := &RelationShip{
				Columns:             local_columns,
				ColumnsNames:        local_columns_names,
				LocalRelation:       table,
				DistantRelation:     distant_table,
				DistantColumns:      distant_columns,
				DistantColumnsNames: distant_columns_names,
				ConstraintName:      constraint.Name,
				IsReverse:           false,
				IsNotNull:           table.Columns[constraint.ColumnsNames[0]].NotNull,
				IsMultiple:          false,
			}

			tables.addRelationship(table, rel)

			rev_rel := &RelationShip{
				Columns:             distant_columns,
				ColumnsNames:        distant_columns_names,
				LocalRelation:       distant_table,
				DistantColumns:      local_columns,
				DistantColumnsNames: local_columns_names,
				DistantRelation:     table,
				ConstraintName:      constraint.Name,
				IsReverse:           true,
				IsNotNull:           !source_is_unique,
				IsMultiple:          !source_is_unique,
			}

			tables.addRelationship(distant_table, rev_rel)
		}
	}

	for _, table := range tables {
		sort.Slice(table.Relationships, func(i, j int) bool {
			return table.Relationships[i].PreferredForm() < table.Relationships[j].PreferredForm()
		})
	}

}

func (tables DBAllTables) addRelationship(table *DBTable, rel *RelationShip) {

	table.Relationships = append(table.Relationships, rel)
	operator := ">>"
	columns := rel.ColumnsNames
	if rel.IsReverse {
		operator = "<<"
		columns = rel.DistantColumnsNames
	}

	normalized_rel_name := rel.LocalRelation.DbTableName() + ".." + strings.Join(columns, "␞") + ".." + strings.Join(rel.DistantColumnsNames, "␞")
	tables.addRelName(table, normalized_rel_name, rel)

	// schema.table
	tables.addRelName(table, operator+rel.DistantRelation.DbTableName(), rel)

	// table
	tables.addRelName(table, operator+rel.DistantRelation.Name, rel)

	// schema.table(columns)
	tables.addRelName(table, operator+rel.DistantRelation.DbTableName()+"("+strings.Join(columns, ",")+")", rel)
	tables.addRelName(table, operator+rel.ConstraintName, rel)

	if rel.IsReverse && rel.IsMultiple {
		tables.addRelName(table, operator+rel.DistantRelation.DbTableName()+"[]", rel)
		// table
		tables.addRelName(table, operator+rel.DistantRelation.Name+"[]", rel)
		tables.addRelName(table, operator+rel.DistantRelation.DbTableName()+"("+strings.Join(columns, ",")+")[]", rel)
		tables.addRelName(table, operator+rel.ConstraintName+"[]", rel)
	}
}

func (rel *RelationShip) PreferredForm() string {
	suffix := ""
	if rel.IsMultiple {
		suffix = "[]"
	}

	prefix := ">> "
	columns := rel.ColumnsNames
	if rel.IsReverse {
		prefix = "<< "
		columns = rel.DistantColumnsNames
	}

	return prefix + rel.DistantRelation.DbTableName() + "(" + strings.Join(columns, ",") + ")" + suffix
}

func (tables DBAllTables) addRelName(table *DBTable, name string, rel *RelationShip) {
	// pp.Println("*** addRelName", name, rel.DistantRelation.Name)
	if _, ok := table.RelationshipsMap[name]; ok {
		// If the relationship already exists, we set it to nil
		// Any request to this relationship will return nil and thus generate an error
		table.RelationshipsMap[name] = nil
	} else {
		table.RelationshipsMap[name] = rel
	}
	// pp.Println("*** addRelName", table.Name, "=>", rel.DistantRelation.Name, name)
}
