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
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/samber/oops"
)

// Introspection of the database.
type DbInfos struct {
	Pool *pgxpool.Pool

	Types []Type

	Functions []*Function
	Relations []*Relation

	// SearchPath is the connecting role's resolved search path, in lookup
	// order — see FillSearchPath (info_searchpath.go).
	SearchPath []string

	TypeMapByOid       map[int]*Type
	RelationMapByRelid map[int]*Relation

	// Built once by buildLookupIndices (info_lookup.go), after Relations/
	// Functions are filled — see ResolveRelation/ResolveFunctionCandidates.
	RelationMapBySchemaName map[string]map[string]*Relation
	FunctionsBySchemaName   map[string]map[string][]*Function
}

func (db *DbInfos) GetType(oid int) *Type {
	if t, ok := db.TypeMapByOid[oid]; ok {
		return t
	}
	return nil
}

func (d *DbInfos) GetRelation(relid int) *Relation {
	if r, ok := d.RelationMapByRelid[relid]; ok {
		return r
	}
	return nil
}

func (d *DbInfos) GetRelationByType(typeOid int) *Relation {
	if t, ok := d.TypeMapByOid[typeOid]; ok {
		return d.GetRelation(t.PgRelId)
	}
	return nil
}

// ------------------------------------------------------------

// Create a database connection and fill the informations
func NewInfos(uri string) (*DbInfos, error) {
	pool, err := pgxpool.New(context.Background(), uri)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to create pool")
	}

	var db = &DbInfos{
		Pool:               pool,
		TypeMapByOid:       make(map[int]*Type),
		RelationMapByRelid: make(map[int]*Relation),
	}

	conn, err := pool.Acquire(context.Background())
	if err != nil {
		return nil, oops.Wrapf(err, "failed to acquire a connection to introspect the database")
	}
	defer conn.Release()

	if err := db.Fill(conn.Conn()); err != nil {
		return nil, err
	}

	return db, nil
}

// Fill informations from the database
func (db *DbInfos) Fill(conn *pgx.Conn) error {
	if err := FillSearchPath(db, conn); err != nil {
		return err
	}

	if err := FillFunctionInformations(db, conn); err != nil {
		return err
	}

	if err := FillRelationInformations(db, conn); err != nil {
		return err
	}

	// Constraints and indexes both need relations already resolved
	// (correlated by PgRelId), but are independent of each other.
	if err := FillConstraintInformations(db, conn); err != nil {
		return err
	}

	if err := FillIndexInformations(db, conn); err != nil {
		return err
	}

	// Types-filling reads back into both Functions and Relations, so it must
	// run after both are populated.
	if err := FillTypeInformations(db, conn); err != nil {
		return err
	}

	buildLookupIndices(db)

	return nil
}
