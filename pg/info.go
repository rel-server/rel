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

	// AnonymousRoleExists reports whether pg.query.anonymous_role was found in pg_roles. false disables anonymous
	// access : every anonymous request gets 401 before route lookup, body read or connection.
	AnonymousRoleExists bool

	// HasCBOR reports whether any type is a CBOR column's type (Type.CBOR).
	HasCBOR bool

	// SearchPath is the connecting role's resolved search path, in lookup order.
	SearchPath []string

	TypeMapByOid       map[int]*Type
	RelationMapByRelid map[int]*Relation

	// Built by buildLookupIndices once Relations/Functions are filled.
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

// NewInfos is NewInfosAdminQuery(uri, uri, 0, "") : one URI, default pool size, no anonymous role.
func NewInfos(uri string) (*DbInfos, error) {
	return NewInfosAdminQuery(uri, uri, 0, "")
}

// NewInfosAdminQuery introspects over a short-lived connection to primaryURI, then builds the returned Pool
// from queryURI. poolSize caps the Pool's MaxConns, 0 keeps pgx's default. anonymousRole fills
// AnonymousRoleExists ; empty leaves it false.
func NewInfosAdminQuery(primaryURI, queryURI string, poolSize int, anonymousRole string, opts ...Option) (*DbInfos, error) {
	o, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	db, err := introspect(context.Background(), primaryURI, anonymousRole, o)
	if err != nil {
		return nil, err
	}

	queryPoolConfig, err := pgxpool.ParseConfig(queryURI)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to parse query pool connection string")
	}
	queryPoolConfig.ConnConfig.OnNotice = o.notices
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

// ReIntrospect builds a fresh *DbInfos like NewInfosAdminQuery, but on the given pool, which the caller keeps
// owning. It resets the pool, since its connections hold temp tables built for the previous schema ; the
// caller must have no request running (a reload's maintenance mode).
func ReIntrospect(ctx context.Context, primaryURI string, pool *pgxpool.Pool, anonymousRole string, opts ...Option) (*DbInfos, error) {
	o, err := applyOptions(opts)
	if err != nil {
		return nil, err
	}
	db, err := introspect(ctx, primaryURI, anonymousRole, o)
	if err != nil {
		return nil, err
	}
	db.Pool = pool
	pool.Reset()
	return db, nil
}

// introspect is NewInfosAdminQuery's/ReIntrospect's shared body ; it leaves Pool unset.
func introspect(ctx context.Context, primaryURI string, anonymousRole string, o *options) (*DbInfos, error) {
	introspectConfig, err := pgxpool.ParseConfig(primaryURI)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to parse introspection connection string")
	}
	introspectConfig.ConnConfig.OnNotice = o.notices
	introspectPool, err := pgxpool.NewWithConfig(ctx, introspectConfig)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to create introspection pool")
	}
	defer introspectPool.Close()

	conn, err := introspectPool.Acquire(ctx)
	if err != nil {
		return nil, oops.Wrapf(err, "failed to acquire a connection to introspect the database")
	}
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

	// Constraints and indexes need relations already resolved.
	if err := FillConstraintInformations(db, conn); err != nil {
		return err
	}

	if err := FillIndexInformations(db, conn); err != nil {
		return err
	}

	// Needs only relations (and their columns' Index).
	FillViewProvenance(db, conn)

	// Types fill back into Functions and Relations, so they come after both.
	if err := FillTypeInformations(db, conn); err != nil {
		return err
	}
	for _, typ := range db.Types {
		log.Debug("type found", "schema", typ.PgIdentifier.Schema, "name", typ.PgIdentifier.Name)
	}

	// Needs types resolved.
	FillComputedFields(db)

	buildLookupIndices(db)

	return nil
}
