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

// Seeds the test/hotel schema with fake data — see test/README.md. The
// actual seeding logic lives in test/seed/seed (package seed), so it can
// also be imported directly (e.g. by the read-path benchmarks in
// query_bench) instead of being duplicated here. This file is just a thin
// CLI wrapper around seed.Run.
//
// Usage : go run ./test/seed <postgres-uri>  (or set DATABASE_URL)
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/rel-server/rel/test/seed/seed"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fixedSeed makes every run reproducible : the volume of data is
// random-looking, but not actually different from one run to the next.
const fixedSeed = 42

func main() {
	uri := os.Getenv("DATABASE_URL")
	if len(os.Args) > 1 {
		uri = os.Args[1]
	}
	if uri == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./test/seed <postgres-uri>  (or set DATABASE_URL)")
		os.Exit(1)
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, uri)
	must(err)
	defer pool.Close()

	must(seed.Run(ctx, pool, fixedSeed))

	fmt.Println("seed complete")
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed failed:", err)
		os.Exit(1)
	}
}
