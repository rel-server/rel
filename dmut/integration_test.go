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

package dmut

import (
	"bytes"
	"context"
	"testing"

	"github.com/ceymard/rel/config"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

// TestRun_AppliesFixtureMutations is an integration test against a real
// postgres container : Run must actually apply testdata/mutations.yml
// (create the "widgets" table), commit it, and forward dmut's own log
// lines through the Output adapter into rel's logger.
func TestRun_AppliesFixtureMutations(t *testing.T) {
	ctx := context.Background()

	container, err := postgres.Run(ctx, "postgres:16-alpine", postgres.BasicWaitStrategies())
	if err != nil {
		t.Fatalf("starting postgres container: %v", err)
	}
	defer func() {
		_ = container.Terminate(ctx)
	}()

	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	var buf bytes.Buffer
	logger := testLogger(&buf)

	ran, err := Run(ctx, uri, config.Dmut{Path: "testdata"}, logger)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ran {
		t.Fatalf("expected ran=true for an existing dmut.path")
	}

	pool, err := pgxpool.New(ctx, uri)
	if err != nil {
		t.Fatalf("pgxpool.New: %v", err)
	}
	defer pool.Close()

	var exists bool
	if err := pool.QueryRow(ctx,
		"select exists(select 1 from information_schema.tables where table_name = 'widgets')",
	).Scan(&exists); err != nil {
		t.Fatalf("checking widgets table: %v", err)
	}
	if !exists {
		t.Fatalf("expected the widgets table to exist after Run applied the fixture mutation")
	}

	if buf.Len() == 0 {
		t.Errorf("expected dmut's own progress lines forwarded into the logger, got nothing")
	}
}

// TestRun_NonexistentPrimaryURIFails covers err being surfaced (not
// swallowed) when the directory exists but the run itself fails — here,
// because there's nothing to connect to at all.
func TestRun_NonexistentPrimaryURIFails(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(&buf)

	ran, err := Run(context.Background(), "postgres://postgres:postgres@127.0.0.1:1/db", config.Dmut{Path: "testdata"}, logger)
	if err == nil {
		t.Fatalf("expected an error when the primary URI can't be reached")
	}
	if !ran {
		t.Fatalf("expected ran=true : the directory exists, the run itself is what failed")
	}
}
