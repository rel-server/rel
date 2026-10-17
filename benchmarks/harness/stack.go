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

package harness

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/test/seed/seed"
	"github.com/samber/oops"
)

// Port the compose file publishes Postgres on.
const dbPort = 15432

// BenchDB is the database every server connects to ; it is a fresh copy of a
// golden database, recreated before each write measurement.
const BenchDB = "bench"

// Sizes maps a data size name to the seed.RunScaled factor.
var Sizes = map[string]int{"small": 1, "medium": 200, "large": 1000}

// serverInfo is how to reach a server and tell it is ready.
type serverInfo struct {
	URL   string
	Probe Request
}

var graphqlProbe = Request{Method: "POST", Path: "/graphql", Header: http.Header{"Content-Type": {"application/json"}}, Body: []byte(`{"query":"{__typename}"}`)}

var servers = map[Server]serverInfo{
	Rel: {"http://localhost:18080", Request{Method: "POST", Path: "/rel", Header: http.Header{"Content-Type": {"application/json"}},
		Body: []byte(`{"relation":"chains","schema":"hotel","limit":1,"select":{"id":["col","id"]}}`)}},
	PostgREST:     {"http://localhost:13000", Request{Method: "GET", Path: "/chains?limit=1"}},
	PostGraphile4: {"http://localhost:15004", graphqlProbe},
	PostGraphile5: {"http://localhost:15005", graphqlProbe},
}

// URL returns the base URL a server listens on.
func URL(s Server) string { return servers[s].URL }

// Stack controls the docker compose stack and the benchmark databases. It is
// not safe for concurrent use : the benchmark runs one thing at a time.
type Stack struct {
	// Dir is the benchmarks directory, where compose.yaml lives.
	Dir string
	Res Resources
	// Verbose streams docker compose output to stderr.
	Verbose bool
}

func (s *Stack) compose(args ...string) error {
	cmd := exec.Command("docker", append([]string{"compose", "-f", filepath.Join(s.Dir, "compose.yaml")}, args...)...)
	cmd.Env = append(os.Environ(),
		"BENCH_DB_CPUSET="+CPUSet(s.Res.DBCPUs),
		"BENCH_SERVER_CPUSET="+CPUSet(s.Res.ServerCPUs),
		"BENCH_DB_NAME="+BenchDB,
	)
	var out []byte
	var err error
	if s.Verbose {
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		err = cmd.Run()
	} else {
		out, err = cmd.CombinedOutput()
	}
	if err != nil {
		return oops.With("args", args, "output", string(out)).Wrapf(err, "docker compose %s", strings.Join(args, " "))
	}
	return nil
}

func adminURI(db string) string {
	return fmt.Sprintf("postgres://postgres:postgres@localhost:%d/%s?sslmode=disable", dbPort, db)
}

// Pool connects to a database of the benchmark Postgres as its superuser.
func (s *Stack) Pool(ctx context.Context, db string) (*pgxpool.Pool, error) {
	p, err := pgxpool.New(ctx, adminURI(db))
	if err != nil {
		return nil, oops.With("db", db).Wrapf(err, "connecting to benchmark database")
	}
	return p, nil
}

// UpDB starts Postgres and waits until it accepts connections.
func (s *Stack) UpDB(ctx context.Context) error {
	if err := s.compose("up", "-d", "--wait", "db"); err != nil {
		return err
	}
	return nil
}

// Down stops every service, keeping the database volume.
func (s *Stack) Down() error { return s.compose("down") }

func exists(ctx context.Context, admin *pgxpool.Pool, db string) (bool, error) {
	var ok bool
	err := admin.QueryRow(ctx, `select exists(select 1 from pg_database where datname = $1)`, db).Scan(&ok)
	return ok, oops.Wrapf(err, "checking database %s", db)
}

func goldenName(size string) string { return "bench_golden_" + size }

// EnsureGolden builds the golden database of the given size if it does not
// exist (or if rebuild is set) : schema, roles, seeded rows, then VACUUM
// ANALYZE so every server plans against real statistics.
func (s *Stack) EnsureGolden(ctx context.Context, size string, rebuild bool) error {
	scale, ok := Sizes[size]
	if !ok {
		return oops.With("size", size).Errorf("unknown data size")
	}
	admin, err := s.Pool(ctx, "postgres")
	if err != nil {
		return err
	}
	defer admin.Close()
	name := goldenName(size)
	have, err := exists(ctx, admin, name)
	if err != nil {
		return err
	}
	if have && !rebuild {
		return nil
	}
	if have {
		if _, err := admin.Exec(ctx, `drop database `+name+` with (force)`); err != nil {
			return oops.Wrapf(err, "dropping %s", name)
		}
	}
	if _, err := admin.Exec(ctx, `create database `+name); err != nil {
		return oops.Wrapf(err, "creating %s", name)
	}
	pool, err := s.Pool(ctx, name)
	if err != nil {
		return err
	}
	defer pool.Close()
	for _, f := range []string{"../test/hotel/schema.sql", "db/roles.sql"} {
		sql, err := os.ReadFile(filepath.Join(s.Dir, f))
		if err != nil {
			return oops.Wrapf(err, "reading %s", f)
		}
		if _, err := pool.Exec(ctx, string(sql), pgx.QueryExecModeSimpleProtocol); err != nil {
			return oops.With("file", f).Wrapf(err, "applying %s", f)
		}
	}
	if err := seed.RunScaled(ctx, pool, 42, scale); err != nil {
		return oops.With("size", size).Wrapf(err, "seeding")
	}
	if _, err := pool.Exec(ctx, `vacuum analyze`); err != nil {
		return oops.Wrapf(err, "vacuum analyze")
	}
	return nil
}

// Reset recreates BenchDB as a copy of the golden database of the given size.
// Every server must be stopped : a template copy and a drop both need the
// databases free of other connections.
func (s *Stack) Reset(ctx context.Context, size string) error {
	admin, err := s.Pool(ctx, "postgres")
	if err != nil {
		return err
	}
	defer admin.Close()
	if _, err := admin.Exec(ctx, `drop database if exists `+BenchDB+` with (force)`); err != nil {
		return oops.Wrapf(err, "dropping %s", BenchDB)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`create database %s template %s`, BenchDB, goldenName(size))); err != nil {
		return oops.With("size", size).Wrapf(err, "cloning golden database")
	}
	return nil
}

// Start (re)builds and starts a server, then waits until it answers its probe.
func (s *Stack) Start(srv Server) error {
	if err := s.compose("up", "-d", "--build", string(srv)); err != nil {
		return err
	}
	info := servers[srv]
	c := NewClient(info.URL)
	defer c.Close()
	deadline := time.Now().Add(90 * time.Second)
	for {
		if _, err := c.Do(info.Probe); err == nil {
			return nil
		} else if time.Now().After(deadline) {
			return oops.With("server", srv).Wrapf(err, "server not ready after 90s")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// Stop stops a server container.
func (s *Stack) Stop(srv Server) error { return s.compose("stop", string(srv)) }

// LoadEnv reads from db what scenarios need to target real rows.
func LoadEnv(ctx context.Context, db *pgxpool.Pool, size string) (*Env, error) {
	e := &Env{Size: size}
	err := db.QueryRow(ctx, `select (select count(*) from hotel.properties), (select count(*) from hotel.guests), (select count(*) from hotel.rooms)`).
		Scan(&e.Properties, &e.Guests, &e.Rooms)
	if err != nil {
		return nil, oops.Wrapf(err, "counting rows")
	}
	rows, err := db.Query(ctx, `select id::text from hotel.bookings order by id limit 2000`)
	if err != nil {
		return nil, oops.Wrapf(err, "sampling bookings")
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, oops.Wrapf(err, "scanning booking id")
		}
		e.BookingIDs = append(e.BookingIDs, id)
	}
	rows.Close()
	for q, dst := range map[string]*[]int64{`select id from hotel.rooms order by id`: &e.RoomIDs, `select id from hotel.guests order by id`: &e.GuestIDs} {
		rows, err := db.Query(ctx, q)
		if err != nil {
			return nil, oops.Wrapf(err, "loading ids")
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				return nil, oops.Wrapf(err, "scanning id")
			}
			*dst = append(*dst, id)
		}
		rows.Close()
	}
	return e, nil
}
