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

package reloadcmd

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rel-server/rel/config"
)

// One postgres container, started by the first test that needs it and
// shared by all : each test works in its own database.
var (
	sharedOnce      sync.Once
	sharedContainer *postgres.PostgresContainer
	sharedHost      string
	sharedPort      string
	sharedErr       error
	dbCounter       atomic.Int32
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedContainer != nil {
		_ = testcontainers.TerminateContainer(sharedContainer)
	}
	os.Exit(code)
}

// newTestDatabase returns a fresh database on the shared container and a
// config pointing at it with the container's superuser.
func newTestDatabase(t *testing.T) (*config.Config, string) {
	t.Helper()
	ctx := context.Background()
	sharedOnce.Do(func() {
		sharedContainer, sharedErr = postgres.Run(ctx, "postgres:16-alpine", postgres.BasicWaitStrategies())
		if sharedErr != nil {
			return
		}
		sharedHost, sharedErr = sharedContainer.Host(ctx)
		if sharedErr != nil {
			return
		}
		port, perr := sharedContainer.MappedPort(ctx, "5432/tcp")
		sharedErr, sharedPort = perr, port.Port()
	})
	if sharedErr != nil {
		t.Fatalf("starting postgres container: %v", sharedErr)
	}

	admin := fmt.Sprintf("postgres://postgres:postgres@%s:%s/postgres?sslmode=disable", sharedHost, sharedPort)
	name := fmt.Sprintf("dmut_test_%d", dbCounter.Add(1))
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connecting as admin: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "create database "+name); err != nil {
		t.Fatalf("creating database: %v", err)
	}

	cfg := config.Test()
	cfg.Pg.User, cfg.Pg.Password = "postgres", "postgres"
	cfg.Pg.URI = fmt.Sprintf("postgres://postgres:postgres@%s:%s/%s?sslmode=disable", sharedHost, sharedPort, name)
	cfg.Dmut.Timeout = 60
	return cfg, name
}

func queryOne(t *testing.T, uri, sql string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, uri)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	defer conn.Close(ctx)
	var out string
	if err := conn.QueryRow(ctx, sql).Scan(&out); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return out
}

// writeMutations writes a dmut yaml file in a fresh directory and returns it.
func writeMutations(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return dir
}

const widgetsMutation = `__namespace: app

app.widgets:
  core:
    - create table app_widgets (id int primary key);
`

func TestDmutURI_LoginFallback(t *testing.T) {
	base := func() *config.Config {
		cfg := &config.Config{}
		cfg.Pg.URI = "postgres://pguser:pgpass@db:5432/mydb?sslmode=disable"
		cfg.Pg.User, cfg.Pg.Password = "pguser", "pgpass"
		return cfg
	}
	cases := []struct {
		name, user, password, wantUser, wantPassword string
	}{
		{"both fall back", "", "", "pguser", "pgpass"},
		{"both overridden", "mig", "s3cr:t@/", "mig", "s3cr:t@/"},
		{"user only, password falls back", "mig", "", "mig", "pgpass"},
		{"password only, user falls back", "", "other", "pguser", "other"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base()
			cfg.Dmut.User, cfg.Dmut.Password = c.user, c.password
			got, err := dmutURI(cfg)
			if err != nil {
				t.Fatalf("dmutURI: %v", err)
			}
			u, err := url.Parse(got)
			if err != nil {
				t.Fatalf("result does not parse: %v", err)
			}
			pw, _ := u.User.Password()
			if u.User.Username() != c.wantUser || pw != c.wantPassword {
				t.Errorf("got login %q/%q, want %q/%q", u.User.Username(), pw, c.wantUser, c.wantPassword)
			}
			if u.Host != "db:5432" || u.Path != "/mydb" || u.Query().Get("sslmode") != "disable" {
				t.Errorf("host, database or query changed: %s", got)
			}
		})
	}
}

func TestSplitPaths(t *testing.T) {
	got := splitPaths("/a::/b:")
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("got %q", got)
	}
	if len(splitPaths("")) != 0 {
		t.Errorf("empty list should yield nothing")
	}
}

func TestRun_EmptyDmutPathSkips(t *testing.T) {
	ran, err := runDmut(context.Background(), &config.Config{}, discardLogger())
	if ran || err != nil {
		t.Errorf("ran=%v err=%v, want skipped", ran, err)
	}
}

func TestRun_Dmut_AppliesMutationsAndTagsModule(t *testing.T) {
	cfg, _ := newTestDatabase(t)
	cfg.Dmut.Path = writeMutations(t, "app.yml", widgetsMutation)

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	ran, err := Run(context.Background(), cfg, logger)
	if err != nil || !ran {
		t.Fatalf("Run: ran=%v err=%v", ran, err)
	}
	if got := queryOne(t, cfg.Pg.URI, "select to_regclass('app_widgets')::text"); got != "app_widgets" {
		t.Errorf("table not created, to_regclass=%q", got)
	}
	if !strings.Contains(buf.String(), "module=dmut") {
		t.Errorf("expected dmut's records tagged module=dmut, got %q", buf.String())
	}
	if strings.Contains(buf.String(), "postgres:postgres") {
		t.Errorf("password leaked in logs: %q", buf.String())
	}

	// A second run against the already-applied schema is a no-op.
	if _, err := Run(context.Background(), cfg, logger); err != nil {
		t.Fatalf("second Run: %v", err)
	}
}

func TestRun_Dmut_UsesItsOwnLogin(t *testing.T) {
	cfg, _ := newTestDatabase(t)
	cfg.Dmut.Path = writeMutations(t, "app.yml", widgetsMutation)

	adminURI := strings.Replace(cfg.Pg.URI, strings.SplitN(cfg.Pg.URI, "/", 4)[3], "postgres?sslmode=disable", 1)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, adminURI)
	if err != nil {
		t.Fatalf("connecting as admin: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, `create role migrator login superuser password 'mig-pass'`); err != nil {
		t.Fatalf("creating role: %v", err)
	}

	cfg.Dmut.User, cfg.Dmut.Password = "migrator", "mig-pass"
	if _, err := runDmut(ctx, cfg, discardLogger()); err != nil {
		t.Fatalf("runDmut: %v", err)
	}
	if got := queryOne(t, cfg.Pg.URI, "select tableowner from pg_tables where tablename = 'app_widgets'"); got != "migrator" {
		t.Errorf("table owner = %q, want migrator", got)
	}
}

func TestRun_Dmut_WrongPasswordFails(t *testing.T) {
	cfg, _ := newTestDatabase(t)
	cfg.Dmut.Path = writeMutations(t, "app.yml", widgetsMutation)
	cfg.Dmut.Password = "not-the-password"

	if _, err := runDmut(context.Background(), cfg, discardLogger()); err == nil {
		t.Fatalf("expected a connection failure with a wrong dmut.password")
	}
}

func TestRun_Dmut_MissingPathFails(t *testing.T) {
	cfg, _ := newTestDatabase(t)
	cfg.Dmut.Path = filepath.Join(t.TempDir(), "does-not-exist")

	ran, err := runDmut(context.Background(), cfg, discardLogger())
	if !ran || err == nil {
		t.Fatalf("ran=%v err=%v, want ran with an error", ran, err)
	}
}

func TestRun_Dmut_TimesOut(t *testing.T) {
	cfg, _ := newTestDatabase(t)
	cfg.Dmut.Path = writeMutations(t, "slow.yml", "__namespace: slow\n\nslow.sleep:\n  core:\n    - up: select pg_sleep(30);\n      down: select 1;\n")
	cfg.Dmut.Timeout = 1

	_, err := runDmut(context.Background(), cfg, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected a timeout error, got %v", err)
	}
}

func TestRun_Dmut_SimpleModeRefusesNonAppend(t *testing.T) {
	cfg, _ := newTestDatabase(t)
	dir := writeMutations(t, "app.yml", widgetsMutation)
	cfg.Dmut.Path = dir
	if _, err := Run(context.Background(), cfg, discardLogger()); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	// Change the table's first core statement : not a pure append.
	changed := strings.Replace(widgetsMutation, "id int primary key", "id bigint primary key", 1)
	if err := os.WriteFile(filepath.Join(dir, "app.yml"), []byte(changed), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.Dmut.Mode = config.DmutModeSimple
	if _, err := Run(context.Background(), cfg, discardLogger()); err == nil {
		t.Fatalf("expected simple mode to refuse a non-append change to core")
	}
}

// Both steps always run, and both failures are reported.
func TestRun_DmutFailureStillRunsReloadCmd(t *testing.T) {
	cfg, _ := newTestDatabase(t)
	cfg.Dmut.Path = filepath.Join(t.TempDir(), "does-not-exist")
	marker := filepath.Join(t.TempDir(), "ran")
	cfg.Reload.Cmd = "touch " + marker
	cfg.Reload.Timeout = 5

	ran, err := Run(context.Background(), cfg, discardLogger())
	if !ran || err == nil {
		t.Fatalf("ran=%v err=%v, want ran with an error", ran, err)
	}
	if _, serr := os.Stat(marker); serr != nil {
		t.Errorf("reload.cmd did not run after dmut failed: %v", serr)
	}

	cfg.Reload.Cmd = "false"
	_, err = Run(context.Background(), cfg, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "dmut failed") || !strings.Contains(err.Error(), "reload.cmd exited") {
		t.Errorf("expected both failures joined, got %v", err)
	}
}

// A failing statement must be identifiable from the logged attributes alone.
func TestErrorAttrs_NamesTheFailingStatement(t *testing.T) {
	cfg, _ := newTestDatabase(t)
	dir := writeMutations(t, "app.yml", "__namespace: app\n\napp.broken:\n  core:\n    - create table broken (\n")
	cfg.Dmut.Path = dir

	_, err := Run(context.Background(), cfg, discardLogger())
	if err == nil {
		t.Fatal("expected a failure")
	}
	got := map[string]any{}
	attrs := ErrorAttrs(err)
	for i := 0; i+1 < len(attrs); i += 2 {
		got[attrs[i].(string)] = attrs[i+1]
	}
	t.Logf("attrs: %v", got)
	for _, k := range []string{"mutation", "sql", "sqlstate"} {
		if _, ok := got[k]; !ok {
			t.Errorf("missing %q in %v", k, got)
		}
	}
}
