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

	"github.com/rel-server/rel/logging"
)

var log = logging.For("pg")

// Introspection of the database.
type DbInfos struct {
	Pool *pgxpool.Pool

	Types []Type

	Functions []*Function
	Relations []*Relation

	// AnonymousRoleExists is specs/authentication.md's "# Roles ##
	// Anonymous role existence" check : whether the configured
	// pg.query.anonymous_role name was found in pg_roles at introspection
	// time. false means anonymous access is disabled outright — every
	// unauthenticated request to /rel and /route alike must be rejected with
	// 401, before route lookup, before any request body is read, before a
	// pool connection is ever acquired.
	AnonymousRoleExists bool

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

// NewInfos is NewInfosAdminQuery(uri, uri, 0, "") — the same URI for
// introspection and the returned pool, pgx's own default pool size, no
// anonymous role check.
func NewInfos(uri string) (*DbInfos, error) {
	return NewInfosAdminQuery(uri, uri, 0, "")
}

// NewInfosAdminQuery introspects via a short-lived connection to
// primaryURI, then builds the returned *DbInfos' long-lived Pool from
// queryURI — the same URI for both when config.PgQuery's own login is
// unset ; see that struct's doc comment for why a second, narrower
// pg.query.* login is optional.
//
// poolSize caps the returned Pool's MaxConns (pg.pool_size) ; 0 leaves
// pgx's own default in place. The introspection connection is never sized
// by this — it's exactly one connection, closed before the query pool
// exists.
//
// anonymousRole is pg.query.anonymous_role's configured value, checked
// against pg_roles on the introspection connection to fill the returned
// *DbInfos' AnonymousRoleExists. Empty leaves it false.
func NewInfosAdminQuery(primaryURI, queryURI string, poolSize int, anonymousRole string) (*DbInfos, error) {
	db, err := introspect(context.Background(), primaryURI, anonymousRole)
	if err != nil {
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

// ReIntrospect rebuilds a FRESH *DbInfos (fresh Types/Functions/Relations/
// lookup maps/AnonymousRoleExists) the same way NewInfosAdminQuery does —
// via a short-lived connection to primaryURI — but reuses the EXISTING pool
// passed in for the returned DbInfos' own Pool field, rather than building
// a new one : the caller (specs/migrations.md ## Reloading step 4) owns that
// pool's lifecycle entirely ; this function never builds or closes one.
func ReIntrospect(ctx context.Context, primaryURI string, pool *pgxpool.Pool, anonymousRole string) (*DbInfos, error) {
	db, err := introspect(ctx, primaryURI, anonymousRole)
	if err != nil {
		return nil, err
	}
	db.Pool = pool
	return db, nil
}

// introspect is NewInfosAdminQuery's/ReIntrospect's shared body. Pool is
// left unset — each caller assigns its own afterward.
func introspect(ctx context.Context, primaryURI string, anonymousRole string) (*DbInfos, error) {
	introspectPool, err := pgxpool.New(ctx, primaryURI)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to create introspection pool")
	}
	defer introspectPool.Close()

	conn, err := introspectPool.Acquire(ctx)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to acquire a connection to introspect the database")
	}
	// defer, not explicit release per return site : db.Fill can panic as
	// well as error, and only defer covers both without leaking conn.
	defer conn.Release()

	var db = &DbInfos{
		TypeMapByOid:       make(map[int]*Type),
		RelationMapByRelid: make(map[int]*Relation),
	}

	if err := db.Fill(conn.Conn()); err != nil {
		return nil, err
	}

	if anonymousRole != "" {
		if err := conn.Conn().QueryRow(ctx,
			"select exists(select 1 from pg_roles where rolname = $1)", anonymousRole,
		).Scan(&db.AnonymousRoleExists); err != nil {
			return nil, oops.Wrapf(err, "failed to check anonymous role existence")
		}
	}

	log.Info("introspection complete",
		"relation_count", len(db.Relations),
		"function_count", len(db.Functions),
		"type_count", len(db.Types),
		"anonymous_role_exists", db.AnonymousRoleExists)

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
	for _, fn := range db.Functions {
		log.Debug("function found", "schema", fn.Identifier.Schema, "name", fn.Identifier.Name, "arity", len(fn.Arguments))
	}

	if err := FillRelationInformations(db, conn); err != nil {
		return err
	}
	for _, rel := range db.Relations {
		log.Debug("relation found", "schema", rel.Identifier.Schema, "name", rel.Identifier.Name, "column_count", len(rel.Columns))
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
	for _, typ := range db.Types {
		log.Debug("type found", "schema", typ.PgIdentifier.Schema, "name", typ.PgIdentifier.Name)
	}

	// Needs every Function's argument types and every Relation's own
	// composite Type already resolved — must run after FillTypeInformations.
	FillComputedFields(db)

	buildLookupIndices(db)

	return nil
}
