package sw

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/fatih/color"
	"github.com/go-chi/chi"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"sales-way.com/server/pg"
	"sales-way.com/server/query"
)

type SwServer struct {
	Router                  chi.Router
	Pool                    *pgxpool.Pool
	Hostnames               []string
	Port                    string
	Schemas                 []string
	Tables                  pg.DBAllTables
	Functions               pg.DBFunctionMap
	DmutStatus              string
	RootScope               *query.Scope
	AvailableLoginFunctions LoginFunctions
}

type LoginFunctions struct {
	Login         bool `json:"login" db:"login"`
	ExternalLogin bool `json:"external_login" db:"external_login"`
	SessionCheck  bool `json:"session_check" db:"session_check"`
}

func init() {
	color.NoColor = false
}

var green = color.New(color.FgHiGreen).SprintFunc()
var red = color.New(color.FgHiRed, color.Bold).SprintFunc()
var yellow = color.New(color.FgHiYellow, color.Bold).SprintFunc()

func (srv *SwServer) LogInfo(msg ...interface{}) {
	msgs := append([]interface{}{green("· ")}, msg...)
	log.Print(msgs...)
}

func (srv *SwServer) LogError(msg ...interface{}) {
	msgs := append([]interface{}{red("⚠ ")}, msg...)
	log.Print(msgs...)
}

func (srv *SwServer) LogWarn(msg ...interface{}) {
	msgs := append([]interface{}{yellow("⚠ ")}, msg...)
	log.Print(msgs...)
}

func (srv *SwServer) HasPg() bool {
	return srv.Pool != nil
}

func (srv *SwServer) PgSimpleExec(ctx context.Context, sql string, args ...interface{}) error {
	if srv.Pool == nil {
		return errors.New("no pg pool")
	}

	conn, err := srv.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx, sql, args...)
	return err
}

func (srv *SwServer) PgSimpleQuery(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	if srv.Pool == nil {
		return nil, errors.New("no pg pool")
	}

	pool, err := srv.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer pool.Release()

	rows, err := pool.Query(ctx, sql, args...)
	return rows, err
}

func (srv *SwServer) PgSimpleQueryRow(ctx context.Context, sql string, args ...interface{}) (pgx.Row, error) {
	if srv.Pool == nil {
		return nil, fmt.Errorf("no pool available")
	}

	pool, err := srv.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer pool.Release()

	return pool.QueryRow(ctx, sql, args...), nil
}

func (srv *SwServer) PgReloadTables() error {
	var err error
	srv.Tables, srv.Functions, err = pg.ReloadTables(srv.Pool, srv.Schemas)
	srv.RootScope = query.NewRootScope(srv.Tables, srv.Functions, srv.Schemas)
	return err
}

// GetEnvOrDefault
// SetRoleCookie
// UnsetCookie
