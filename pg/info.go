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
	"gitlab.com/tozd/go/errors"
)

// Introspection of the database.
type DbInfos struct {
	Pool *pgxpool.Pool

	Types []Type

	Functions []*Function
	Relations []*Relation

	TypeMapByOid       map[int]*Type
	RelationMapByRelid map[int]*Relation
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
		return nil, errors.Errorf("failed to create pool: %w", err)
	}

	var db = &DbInfos{
		Pool:               pool,
		TypeMapByOid:       make(map[int]*Type),
		RelationMapByRelid: make(map[int]*Relation),
	}

	conn, err := pool.Acquire(context.Background())
	if err != nil {
		panic(err)
	}
	defer conn.Release()

	if err := db.Fill(conn.Conn()); err != nil {
		return nil, err
	}

	return db, nil
}

// Fill informations from the database
func (db *DbInfos) Fill(conn *pgx.Conn) error {

	// for _, t := range db.Types {
	// 	db.TypeMap[t.oid] = &t
	// }

	if err := FillFunctionInformations(db, conn); err != nil {
		return err
	}

	if err := FillRelationInformations(db, conn); err != nil {
		return err
	}

	if err := FillTypeInformations(db, conn); err != nil {
		return err
	}

	// for _, f := range db.Functions {
	// 	db.FunctionMap[f.Identifier.String()] = &f
	// }

	// if err := FillRelationInformations(infos, conn); err != nil {
	// 	return err
	// }

	return nil
}
