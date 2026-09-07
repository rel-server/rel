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

// Package query_bench holds READ-path benchmarks (query.CompileSelect +
// execution) against the hotel/booking fixture (test/hotel, test/seed/seed —
// see test/README.md), at a realistic data volume : ~13 properties, ~195
// rooms, 200 guests, 500 bookings, plus reviews/payments/staff.
//
// Deliberately a SEPARATE package from query/write_bench_test.go's own
// benchmarks : those exercise the WRITE path (ExecuteWrite) against the
// flat movie/director fixture already wired up by query's own TestMain
// (pg/testdata/schema.sql). This package needs its own container, its own
// schema, and a seed pass — genuinely different setup, so a separate
// package/TestMain keeps the two from being conflated, and keeps the (real)
// container+seed cost isolated to benchmark runs of this package alone.
//
// Run with e.g.:
//
//	go test ./query_bench/... -run=^$ -bench=. -benchtime=1x
package query_bench

import (
	"context"
	"testing"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/test/seed/seed"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

var (
	testDb  *pg.DbInfos
	testCfg *config.Config
)

// fixedSeed matches test/seed/main.go's own const — reproducible data, not
// a coincidence of two independent random choices.
const fixedSeed = 42

// TestMain builds and seeds the hotel schema ONCE for the whole package's
// benchmark run — re-paying that cost per function would dominate the numbers.
func TestMain(m *testing.M) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine", postgres.BasicWaitStrategies(),
		postgres.WithInitScripts("../test/hotel/schema.sql"))
	if err != nil {
		panic(err)
	}
	defer func() {
		_ = container.Terminate(ctx)
	}()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}

	testDb, err = pg.NewInfos(uri)
	if err != nil {
		panic(err)
	}

	if err := seed.Run(ctx, testDb.Pool, fixedSeed); err != nil {
		panic(err)
	}

	testCfg = config.Test()

	// m.Run() (not os.Exit(m.Run())), matching query/node_resolve_test.go's
	// own TestMain : os.Exit would skip the container.Terminate defer above.
	m.Run()
}
