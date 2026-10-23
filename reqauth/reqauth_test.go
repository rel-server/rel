package reqauth

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/rel-server/rel/config"
	"github.com/rel-server/rel/errcode"
	jwtpkg "github.com/rel-server/rel/jwt"
	"github.com/rel-server/rel/pg"
	"github.com/rel-server/rel/relexec"
)

var (
	testDb  *pg.DbInfos
	testCfg *config.Config
)

func TestMain(m *testing.M) {
	ctx := context.Background()
	container, err := postgres.Run(ctx, "postgres:16-alpine",
		postgres.WithOrderedInitScripts("../pg/testdata/schema.sql", "testdata/roles.sql"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		panic(err)
	}
	defer func() { _ = container.Terminate(ctx) }()
	uri, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		panic(err)
	}
	if testDb, err = pg.NewInfos(uri); err != nil {
		panic(err)
	}
	testCfg = config.Test()
	testCfg.Pg.Query.AnonymousRole = "~anonymous"
	m.Run()
}

// requestWith is a request carrying a valid session cookie for claims.
func requestWith(t *testing.T, claims jwtpkg.Claims) *http.Request {
	t.Helper()
	token, err := jwtpkg.Sign(testCfg.Jwt, claims)
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: testCfg.Jwt.CookieName, Value: token})
	return r
}

func memberClaims() jwtpkg.Claims {
	return jwtpkg.Mint(testCfg.Jwt, "member_role", time.Now(), 3600, map[string]any{"sub": "u1"})
}

func asError(t *testing.T, err error) *Error {
	t.Helper()
	e, ok := errors.AsType[*Error](err)
	if !ok {
		t.Fatalf("want *reqauth.Error, got %T: %v", err, err)
	}
	return e
}

func TestAuthenticate_VerifiedUserGetsTheirRole(t *testing.T) {
	db := &pg.DbInfos{AnonymousRoleExists: true}
	s, err := Authenticate(testCfg, db, requestWith(t, memberClaims()))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Verified || s.Role != "member_role" || s.Claims["sub"] != "u1" {
		t.Errorf("session = %+v", s)
	}
}

func TestAuthenticate_AnonymousUserGetsTheAnonymousRole(t *testing.T) {
	db := &pg.DbInfos{AnonymousRoleExists: true}
	s, err := Authenticate(testCfg, db, httptest.NewRequest("GET", "/", nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.Verified || s.Role != "~anonymous" {
		t.Errorf("session = %+v", s)
	}
}

// An anonymous user is refused with 401 when the anonymous role does not exist, and the session
// is still returned so the caller can log it.
func TestAuthenticate_AnonymousRefusedWithoutAnonymousRole(t *testing.T) {
	s, err := Authenticate(testCfg, &pg.DbInfos{}, httptest.NewRequest("GET", "/", nil))
	e := asError(t, err)
	if e.Status != http.StatusUnauthorized || e.Code != errcode.AnonymousDisabled || e.Message != errcode.AnonymousDisabledMessage {
		t.Errorf("error = %+v", e)
	}
	if s == nil || s.Verified {
		t.Errorf("session = %+v, want a non-nil anonymous session", s)
	}
}

// A verified user is let in even when the anonymous role does not exist.
func TestAuthenticate_VerifiedUserNeedsNoAnonymousRole(t *testing.T) {
	if _, err := Authenticate(testCfg, &pg.DbInfos{}, requestWith(t, memberClaims())); err != nil {
		t.Fatal(err)
	}
}

func TestAuthenticate_BadSignatureIsAnonymous(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.AddCookie(&http.Cookie{Name: testCfg.Jwt.CookieName, Value: "not.a.token"})
	s, err := Authenticate(testCfg, &pg.DbInfos{AnonymousRoleExists: true}, r)
	if err != nil || s.Verified {
		t.Errorf("session = %+v, err = %v", s, err)
	}
}

func TestBegin_NoRoleIsRefused(t *testing.T) {
	s := NewSession(config.Test(), nil, false) // anonymous_role unset
	e := asError(t, s.Begin(t.Context(), nil))
	if e.Code != errcode.NoRoleConfigured || e.Status != http.StatusInternalServerError {
		t.Errorf("error = %+v", e)
	}
}

// Renewing writes the cookie but leaves the claims the session carries untouched: SQL and
// request.jwt see the claims as received.
func TestRenew_OnlyWritesTheCookie(t *testing.T) {
	cfg := *testCfg
	cfg.Jwt.RenewAfter = 0.1
	claims := memberClaims()
	claims["iat"] = float64(time.Now().Add(-30 * time.Minute).Unix())
	claims["exp"] = float64(time.Now().Add(30 * time.Minute).Unix())
	iat := claims["iat"]

	s := NewSession(&cfg, claims, true)
	rec := httptest.NewRecorder()
	s.Renew(rec)
	if len(rec.Result().Cookies()) != 1 {
		t.Fatalf("want one Set-Cookie, got %v", rec.Result().Header["Set-Cookie"])
	}
	if s.Claims["iat"] != iat {
		t.Errorf("Renew changed the session's iat: %v -> %v", iat, s.Claims["iat"])
	}
}

func TestRenew_AnonymousWritesNothing(t *testing.T) {
	rec := httptest.NewRecorder()
	NewSession(testCfg, nil, false).Renew(rec)
	if len(rec.Result().Header["Set-Cookie"]) != 0 {
		t.Errorf("anonymous got a cookie: %v", rec.Result().Header["Set-Cookie"])
	}
}

// whoami returns what the function sees from inside the transaction.
func whoami(t *testing.T, tx *Tx) (user string, claims map[string]any) {
	t.Helper()
	var out struct {
		User   string         `json:"user"`
		Claims map[string]any `json:"claims"`
	}
	row := tx.QueryRow(t.Context(), "select whoami()")
	if err := row.Scan(&out); err != nil {
		t.Fatalf("whoami: %v", err)
	}
	return out.User, out.Claims
}

func TestOpen_RunsAsTheRoleWithTheClaims(t *testing.T) {
	s := NewSession(testCfg, memberClaims(), true)
	tx, err := s.Open(t.Context(), testDb.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Release()
	defer func() { _ = tx.Rollback(t.Context()) }()
	user, claims := whoami(t, tx)
	if user != "member_role" || claims["sub"] != "u1" {
		t.Errorf("user = %q, claims = %v", user, claims)
	}
}

func TestOpen_AnonymousRunsAsTheAnonymousRoleWithNullClaims(t *testing.T) {
	s := NewSession(testCfg, nil, false)
	tx, err := s.Open(t.Context(), testDb.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Release()
	defer func() { _ = tx.Rollback(t.Context()) }()
	user, claims := whoami(t, tx)
	if user != "~anonymous" || claims != nil {
		t.Errorf("user = %q, claims = %v", user, claims)
	}
}

// Role and claims are transaction-scoped: after Commit the connection is back to its own role and
// has no claims, whoever takes it from the pool next.
func TestOpen_NothingLeaksPastCommit(t *testing.T) {
	ctx := t.Context()
	pool := testDb.Pool
	if pool.Config().MaxConns > 1 {
		cfg := pool.Config()
		cfg.MaxConns = 1
		p, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer p.Close()
		pool = p
	}
	tx, err := NewSession(testCfg, memberClaims(), true).Open(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx.Release()

	var user string
	var claims *string
	if err := pool.QueryRow(ctx, "select current_user, current_setting('rel.jwt.claims', true)").Scan(&user, &claims); err != nil {
		t.Fatal(err)
	}
	if user != "postgres" || (claims != nil && *claims != "") {
		t.Errorf("leaked: user = %q, claims = %v", user, claims)
	}
}

func TestTx_RollbackAfterCommitIsHarmless(t *testing.T) {
	ctx := t.Context()
	tx, err := NewSession(testCfg, memberClaims(), true).Open(ctx, testDb.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Release()
	if _, err := tx.Exec(ctx, "insert into reqauth_t values (1)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Errorf("Rollback after Commit = %v", err)
	}
	var n int
	if err := testDb.Pool.QueryRow(ctx, "select count(*) from reqauth_t where n = 1").Scan(&n); err != nil || n != 1 {
		t.Errorf("committed row: n = %d, err = %v", n, err)
	}
}

func TestTx_RollbackDiscards(t *testing.T) {
	ctx := t.Context()
	tx, err := NewSession(testCfg, memberClaims(), true).Open(ctx, testDb.Pool)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Release()
	if _, err := tx.Exec(ctx, "insert into reqauth_t values (2)"); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := testDb.Pool.QueryRow(ctx, "select count(*) from reqauth_t where n = 2").Scan(&n); err != nil || n != 0 {
		t.Errorf("rolled back row: n = %d, err = %v", n, err)
	}
}

// A role the login role cannot become fails at the role step, as a Postgres error for the caller
// to classify.
func TestOpen_UnknownRoleIsAPostgresError(t *testing.T) {
	claims := jwtpkg.Mint(testCfg.Jwt, "no_such_role", time.Now(), 3600, nil)
	_, err := NewSession(testCfg, claims, true).Open(t.Context(), testDb.Pool)
	e := asError(t, err)
	if !e.Postgres || e.Message != "applying role" {
		t.Errorf("error = %+v", e)
	}
}

// After a failed Open the pool still hands out a clean connection.
func TestOpen_FailureLeavesNoTransactionBehind(t *testing.T) {
	claims := jwtpkg.Mint(testCfg.Jwt, "no_such_role", time.Now(), 3600, nil)
	for range 3 {
		if _, err := NewSession(testCfg, claims, true).Open(t.Context(), testDb.Pool); err == nil {
			t.Fatal("want an error")
		}
	}
	var status string
	if err := testDb.Pool.QueryRow(t.Context(), "select current_user").Scan(&status); err != nil || status != "postgres" {
		t.Errorf("current_user = %q, err = %v", status, err)
	}
}

func TestRelexecBegin_FailureIsARelexecError(t *testing.T) {
	err := NewSession(config.Test(), nil, false).RelexecBegin()(t.Context(), nil)
	e, ok := errors.AsType[*relexec.Error](err)
	if !ok || e.Code != errcode.NoRoleConfigured || e.Fault != relexec.ServerFault {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(e.Err.Error(), "no role configured") {
		t.Errorf("message = %q", e.Err)
	}
}
