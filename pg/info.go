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

// NewInfos is NewInfosAdminQuery with the same URI used for both
// introspection and the returned pool — the common case for anything that
// doesn't distinguish a primary login from an optional, narrower
// pg.query.* login (every test fixture in this repo connects as one
// superuser either way) — and pgx's own default pool size (poolSize 0,
// see NewInfosAdminQuery), since tests have no config.Pg.PoolSize to read.
func NewInfos(uri string) (*DbInfos, error) {
	return NewInfosAdminQuery(uri, uri, 0)
}

// NewInfosAdminQuery introspects the database via a short-lived connection
// to primaryURI, then builds the returned *DbInfos' own long-lived Pool
// from queryURI instead. cmd/rel's real wiring passes the SAME URI for
// both whenever config.PgQuery's own login is unset (the common,
// simplest-possible-setup case — see that struct's own doc comment for why
// a second, narrower login for request-serving is optional, never
// required) ; when a deployment DOES configure pg.query.*, this keeps that
// choice meaningful : introspection and dmut migrations always run as the
// primary login, and only the pool that actually SERVES requests (whose
// connections have their role SET per-request) uses the narrower one.
// Using the same login for both regardless would make the primary login
// the de facto base identity for every request either way, quietly
// defeating the reason a separate pg.query.* login exists at all.
//
// poolSize caps the returned Pool's MaxConns (pg.pool_size) — 0 leaves
// pgx's own default (max(4, runtime.NumCPU())) in place, never a real
// deployment's own intent, only convenient for tests/NewInfos callers
// with no config to read a size from. The introspection pool above is
// never sized by this : it opens exactly one connection, does its work,
// and closes before the query pool is even created.
func NewInfosAdminQuery(primaryURI, queryURI string, poolSize int) (*DbInfos, error) {
	introspectPool, err := pgxpool.New(context.Background(), primaryURI)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to create introspection pool")
	}
	defer introspectPool.Close()

	conn, err := introspectPool.Acquire(context.Background())
	if err != nil {
		return nil, oops.Wrapf(err, "failed to acquire a connection to introspect the database")
	}
	// defer, not explicit calls at each return site : db.Fill can panic
	// (e.g. resolveColumns indexing into relation.ColumnsMap) as well as
	// return an error, and only defer covers both — an explicit
	// conn.Release() before each return would leak the connection (and
	// block introspectPool.Close() above) on a panic.
	defer conn.Release()

	var db = &DbInfos{
		TypeMapByOid:       make(map[int]*Type),
		RelationMapByRelid: make(map[int]*Relation),
	}

	if err := db.Fill(conn.Conn()); err != nil {
		return nil, err
	}

	queryPoolConfig, err := pgxpool.ParseConfig(queryURI)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to parse query pool connection string")
	}
	if poolSize > 0 {
		queryPoolConfig.MaxConns = int32(poolSize)
	}
	queryPool, err := pgxpool.NewWithConfig(context.Background(), queryPoolConfig)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to create query pool")
	}
	db.Pool = queryPool

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
