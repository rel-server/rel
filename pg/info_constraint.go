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
	"gitlab.com/tozd/go/errors"
)

/** Constraints coming from the database before resolving them. They are not *exactly* like  */
type DBConstraint struct {
	Name              string
	Type              string // PRIMARY KEY, UNIQUE, FOREIGN KEY, INDEX
	Columns           []string
	ColumnsSortedName string

	// Only valid for foreign keys
	TargetRelId             int
	TargetColumns           []string
	TargetColumnsSortedName string
}

type ConstraintType int

const (
	ConstraintTypePrimaryKey ConstraintType = iota
	ConstraintTypeUnique
	ConstraintTypeOutgoingForeignKey
	ConstraintTypeIncomingForeignKey
	ConstraintTypeIndex
)

/** */
type Constraint struct {
	Relation *Relation
	Name     string
	Type     ConstraintType

	Columns           []*Column
	ColumnsAreUnique  bool
	ColumnsSortedName string

	Target *Constraint
}

func (c ConstraintType) String() string {
	return []string{
		"PRIMARY KEY",
		"UNIQUE",
		"FOREIGN KEY",
		"INCOMING FOREIGN KEY",
		"INDEX",
	}[c]
}

func (c ConstraintType) IsOutgoingForeignKey() bool {
	return c == ConstraintTypeOutgoingForeignKey
}

func (c ConstraintType) IsIncomingForeignKey() bool {
	return c == ConstraintTypeIncomingForeignKey
}

func (c ConstraintType) IsPrimaryKey() bool {
	return c == ConstraintTypePrimaryKey
}

func (c ConstraintType) IsUnique() bool {
	return c == ConstraintTypeUnique
}

func (c ConstraintType) IsIndex() bool {
	return c == ConstraintTypeIndex
}

// Fill the relations with constraints correctly resolved to the column objects and fill the constraint map with the names.
func processConstraints(infos *DbInfos) error {

	type tableIncomingUnique struct {
		relation *Relation
		incoming *Constraint
	}

	incoming_to_check := make([]tableIncomingUnique, 0)

	for _, relation := range infos.Relations {
		for _, db_constraint := range relation.AllConstraints {
			var c = &Constraint{
				Relation:          relation,
				Name:              db_constraint.Name,
				ColumnsSortedName: db_constraint.ColumnsSortedName,
				// Type:                   db_constraint.Type,
			}

			relation.ConstraintsByName[c.Name] = c

			// Resolve columns and target columns
			for _, column_name := range db_constraint.Columns {
				c.Columns = append(c.Columns, relation.ColumnsMap[column_name])
			}

			switch db_constraint.Type {
			case "PRIMARY KEY":
				c.Type = ConstraintTypePrimaryKey
				relation.PrimaryKey = c
				relation.UniqueColumnGroups[c.ColumnsSortedName] = c
			case "FOREIGN KEY":
				relation.OutgoingForeignKeys = append(relation.OutgoingForeignKeys, c)

				// When there is a foreign key, we need to create the incoming foreign key on the distant relation.

				incoming := &Constraint{
					Type:              ConstraintTypeIncomingForeignKey,
					Name:              db_constraint.Name,
					ColumnsSortedName: db_constraint.TargetColumnsSortedName,
					Target:            c,
				}

				c.Target = incoming

				if db_constraint.TargetRelId == 0 {
					return errors.Errorf("a foreign key constraint should have a TargetRelId in %s / %s", relation.Identifier.Name, db_constraint.Name)
				}

				// Fill the incoming columns
				target_rel := infos.GetRelation(db_constraint.TargetRelId)
				incoming.Relation = target_rel
				if target_rel == nil {
					return errors.Errorf("failed to find target relation %d (this should not happen)", db_constraint.TargetRelId)
				}

				for _, column_name := range db_constraint.TargetColumns {
					incoming.Columns = append(incoming.Columns, target_rel.ColumnsMap[column_name])
				}

				target_rel.IncomingForeignKeys = append(target_rel.IncomingForeignKeys, incoming)

				// We'll check later if this one has a unicity constraint placed on it
				incoming_to_check = append(incoming_to_check, tableIncomingUnique{
					relation: target_rel,
					incoming: incoming,
				})

				// table_columns_fkey
				relation.AddConstraintByName(c.Name, c)
				// distant_table
				relation.AddConstraintByName(c.Relation.Identifier.Name, c)
				// schema.distant_table
				relation.AddConstraintByName(c.Relation.Identifier.String(), c)
				// distant_table(column1,column2,...)
				relation.AddConstraintByName(c.Target.Relation.Identifier.Name+"("+c.Target.ColumnsSortedName+")", c.Target)
				// schema.distant_table(column1,column2,...)
				relation.AddConstraintByName(c.Target.Relation.Identifier.String()+"("+c.Target.ColumnsSortedName+")", c.Target.Target)

				// Add similar constraints for the distant relation
				target_rel.AddConstraintByName(c.Name, c)
				target_rel.AddConstraintByName(c.Target.Relation.Identifier.Name, c)
				target_rel.AddConstraintByName(c.Target.Relation.Identifier.String(), c)
				target_rel.AddConstraintByName(c.Target.Relation.Identifier.Name+"("+c.Target.ColumnsSortedName+")", c.Target)
				target_rel.AddConstraintByName(c.Target.Relation.Identifier.String()+"("+c.Target.ColumnsSortedName+")", c.Target.Target)

			case "INDEX":
				relation.Indexes = append(relation.Indexes, c)
				relation.AddConstraintByName(c.Name, c)
				relation.AddConstraintByName(c.ColumnsSortedName, c)

			case "UNIQUE":

				// If there is already a relation that goes by that name, then just tell it the columns are unique
				// UNIQUE constraints are always processed last because of the asked sort order.
				if orig, ok := relation.ConstraintsByName[c.ColumnsSortedName]; ok {
					orig.ColumnsAreUnique = true
					c = orig
				} else {
					c.Type = ConstraintTypeUnique
				}

				relation.UniqueColumnGroups[c.ColumnsSortedName] = c
			}
		}
	}

	// Check whether incoming targets are effectively unique groups
	for _, inc := range incoming_to_check {
		if _, ok := inc.relation.UniqueColumnGroups[inc.incoming.ColumnsSortedName]; ok {
			inc.incoming.ColumnsAreUnique = true
		}
	}

	return nil
}
