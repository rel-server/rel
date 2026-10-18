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

// Package profile answers where the time of one request goes, for rel in JSON and in CBOR : it runs rel's real
// handler in this process (over loopback HTTP, one client, requests built beforehand) against a Postgres whose
// pg_stat_statements says how long the database itself worked. Run it with
//
//	PROFILE_DIR=/some/dir go test ./benchmarks/internal/profile -run TestProfile -v -count=1 -timeout 30m
//
// and read the CPU profiles it leaves there with `go tool pprof`.
package profile

import (
	"context"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime/pprof"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rel-server/rel/benchmarks/harness"
	"github.com/rel-server/rel/benchmarks/scenarios"
	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/server"
	"github.com/rel-server/rel/test/seed/seed"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

func cpuTime() time.Duration {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

type ops struct {
	scenario string
	n        int
}

func TestProfile(t *testing.T) {
	dir := os.Getenv("PROFILE_DIR")
	if dir == "" {
		t.Skip("set PROFILE_DIR to run the profile")
	}
	ctx := context.Background()
	c, err := postgres.Run(ctx, "postgres:16-alpine", postgres.BasicWaitStrategies(),
		testcontainers.WithCmd("postgres", "-c", "shared_preload_libraries=pg_stat_statements", "-c", "pg_stat_statements.track=all"),
		postgres.WithOrderedInitScripts("../../../test/hotel/schema.sql", "../../db/roles.sql"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Terminate(ctx) }()
	adminURI, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := pgxpool.New(ctx, adminURI)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	if err := seed.RunScaled(ctx, admin, 42, 1); err != nil {
		t.Fatal(err)
	}
	idx, _ := os.ReadFile("../../db/indexes.sql")
	for _, q := range []string{string(idx), "create extension pg_stat_statements", "vacuum analyze"} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	queryURI := strings.Replace(adminURI, "postgres:postgres@", "bench:bench@", 1)
	db, err := pg.NewInfosAdminQuery(adminURI, queryURI, 0, "~anonymous")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Test()
	cfg.Pg.Query.AnonymousRole = "~anonymous"
	ts := httptest.NewServer(server.NewRelHandler(db, cfg, nil))
	defer ts.Close()

	env, err := harness.LoadEnv(ctx, admin, "small")
	if err != nil {
		t.Fatal(err)
	}
	all := map[string]harness.Scenario{}
	for _, sc := range scenarios.All() {
		all[sc.Name] = sc
	}

	// what a trip to the database costs on this machine, and a commit that writes : the floor under every request
	conn, err := pgxpool.New(ctx, queryURI)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	timeIt := func(label string, n int, f func(i int) error) {
		for i := range n {
			if err := f(i); err != nil {
				t.Fatal(err)
			}
		}
		t0 := time.Now()
		for i := range n {
			if err := f(n + i); err != nil {
				t.Fatal(err)
			}
		}
		fmt.Printf("baseline: %-46s %7.0f µs\n", label, float64(time.Since(t0).Microseconds())/float64(n))
	}
	timeIt("select 1 (one round trip)", 5000, func(int) error { _, err := conn.Exec(ctx, "select 1"); return err })
	timeIt("insert, autocommit (one round trip + commit)", 1000, func(i int) error {
		_, err := conn.Exec(ctx, "insert into hotel.guests (email, first_name, last_name) values ($1, 'a', 'b')", fmt.Sprintf("base-%d@x.org", i))
		return err
	})
	timeIt("update by pk, autocommit", 1000, func(i int) error {
		_, err := conn.Exec(ctx, "update hotel.guests set phone = $1 where id = $2", fmt.Sprint(i), env.GuestIDs[i%len(env.GuestIDs)])
		return err
	})

	fmt.Printf("\n%-30s %-9s %9s %9s %9s %9s %7s %9s\n", "scenario", "format", "wall µs", "cpu µs", "db µs", "wall-db", "stmts", "bytes")
	var base uint64
	for _, o := range []ops{
		{"read/pk-lookup", 15000}, {"read/filter-sort-paginate", 12000}, {"read/deep-nested-embed", 6000},
		{"read/wide-unscoped", 1500},
		{"write/insert-one", 2500}, {"write/update-by-pk", 2500}, {"write/bulk-insert-100", 1200}, {"write/nested-three-level", 1000},
	} {
		sc := all[o.scenario]
		for _, srv := range profiled() {
			impl := sc.Impls[srv]
			client := harness.NewClientFor(ts.URL, srv)
			// build every request beforehand, so neither marshalling nor CBOR conversion is measured
			reqs := make([]harness.Request, o.n)
			for i := range reqs {
				base++
				r, ok := client.Prepare(impl, env, base)
				if !ok {
					t.Fatalf("%s is not a single request", o.scenario)
				}
				reqs[i] = r
			}
			if !sc.Write { // warm up (a write cannot be sent twice)
				for range 20 {
					if _, err := client.Send(reqs[0]); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := admin.Exec(ctx, "select pg_stat_statements_reset()"); err != nil {
				t.Fatal(err)
			}
			name := strings.ReplaceAll(o.scenario, "/", "-") + "." + string(srv)
			f, _ := os.Create(filepath.Join(dir, name+".prof"))
			_ = pprof.StartCPUProfile(f)
			bytes0, cpu0, t0 := client.Bytes(), cpuTime(), time.Now()
			for _, r := range reqs {
				if _, err := client.Send(r); err != nil {
					pprof.StopCPUProfile()
					t.Fatalf("%s %s: %v", o.scenario, srv, err)
				}
			}
			wall, cpu := time.Since(t0), cpuTime()-cpu0
			pprof.StopCPUProfile()
			_ = f.Close()
			var dbMs float64
			var calls int64
			if err := admin.QueryRow(ctx, `select coalesce(sum(total_exec_time),0), coalesce(sum(calls),0)::bigint from pg_stat_statements
				where dbid = (select oid from pg_database where datname = current_database()) and query not like '%pg_stat_statements%'`).Scan(&dbMs, &calls); err != nil {
				t.Fatal(err)
			}
			n := float64(o.n)
			us := func(d time.Duration) float64 { return float64(d.Microseconds()) / n }
			dbUs := dbMs * 1000 / n
			fmt.Printf("%-30s %-9s %9.0f %9.0f %9.0f %9.0f %7.1f %9.0f\n", o.scenario, srv, us(wall), us(cpu), dbUs, us(wall)-dbUs, float64(calls)/n, float64(client.Bytes()-bytes0)/n)
		}
	}
}

// profiled lists the servers to profile : both formats, or only JSON with PROFILE_JSON_ONLY set (a server without
// CBOR, such as an older revision).
func profiled() []harness.Server {
	if os.Getenv("PROFILE_JSON_ONLY") != "" {
		return []harness.Server{harness.Rel}
	}
	return []harness.Server{harness.Rel, harness.RelCBOR}
}
