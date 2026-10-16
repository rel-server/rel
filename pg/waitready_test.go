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
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsTransientConnectError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"refused", &net.OpError{Op: "dial", Err: errors.New("connection refused")}, true},
		{"dns", &net.DNSError{Err: "no such host", Name: "db"}, true},
		{"starting up", &pgconn.PgError{Code: "57P03"}, true},
		{"bad password", &pgconn.PgError{Code: "28P01"}, false},
		{"missing database", &pgconn.PgError{Code: "3D000"}, false},
		{"unknown", errors.New("boom"), false},
	}
	for _, c := range cases {
		if got := isTransientConnectError(c.err); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWaitReady_Schedule(t *testing.T) {
	transient := &pgconn.PgError{Code: "57P03"}
	var sleeps []time.Duration
	calls := 0
	err := waitReady(context.Background(), time.Hour, func(d time.Duration) { sleeps = append(sleeps, d) },
		func(context.Context) error {
			calls++
			if calls < 8 {
				return transient
			}
			return nil
		})
	if err != nil || calls != 8 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	want := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 4 * time.Second, 5 * time.Second, 5 * time.Second, 5 * time.Second}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps %v", sleeps)
	}
	for i := range want {
		if sleeps[i] != want[i] {
			t.Fatalf("sleeps %v, want %v", sleeps, want)
		}
	}
}

func TestWaitReady_PermanentErrorFailsAtOnce(t *testing.T) {
	calls := 0
	err := waitReady(context.Background(), time.Hour, func(time.Duration) { t.Fatal("slept") },
		func(context.Context) error { calls++; return &pgconn.PgError{Code: "28P01"} })
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWaitReady_ZeroBudgetTriesOnce(t *testing.T) {
	calls := 0
	err := waitReady(context.Background(), 0, func(time.Duration) { t.Fatal("slept") },
		func(context.Context) error { calls++; return &pgconn.PgError{Code: "57P03"} })
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWaitReady_BudgetExhaustedReturnsLastError(t *testing.T) {
	err := waitReady(context.Background(), 50*time.Millisecond, time.Sleep,
		func(context.Context) error { return &pgconn.PgError{Code: "57P03"} })
	if err == nil {
		t.Fatal("expected error")
	}
}

// Integration : nothing listens at first, then a listener appears on the
// same port ; the wait must succeed once Postgres is reachable. The real
// test container stands in for the late-starting server via a TCP proxy.
func TestWaitReady_Integration_LateStart(t *testing.T) {
	u, err := url.Parse(testDbURI)
	if err != nil {
		t.Fatal(err)
	}
	upstream := u.Host
	// Reserve a free port, release it, and only start proxying later.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()

	go func() {
		time.Sleep(1500 * time.Millisecond)
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return
		}
		t.Cleanup(func() { _ = ln.Close() })
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				up, err := net.Dial("tcp", upstream)
				if err != nil {
					_ = c.Close()
					return
				}
				go func() { _, _ = copyConn(up, c); _ = up.Close() }()
				_, _ = copyConn(c, up)
				_ = c.Close()
			}()
		}
	}()

	u.Host = addr
	if err := WaitReady(context.Background(), u.String(), 20*time.Second); err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
}

func TestWaitReady_Integration_BadPasswordFailsFast(t *testing.T) {
	u, _ := url.Parse(testDbURI)
	u.User = url.UserPassword(u.User.Username(), "definitely-wrong")
	start := time.Now()
	err := WaitReady(context.Background(), u.String(), 20*time.Second)
	if err == nil || !strings.Contains(err.Error(), "28P01") {
		t.Fatalf("expected 28P01, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("did not fail fast: %v", time.Since(start))
	}
}

func TestWaitReady_Integration_Ready(t *testing.T) {
	if err := WaitReady(context.Background(), testDbURI, 0); err != nil {
		t.Fatal(err)
	}
}

func copyConn(dst, src net.Conn) (int64, error) {
	buf := make([]byte, 32*1024)
	var n int64
	for {
		r, err := src.Read(buf)
		if r > 0 {
			w, werr := dst.Write(buf[:r])
			n += int64(w)
			if werr != nil {
				return n, werr
			}
		}
		if err != nil {
			return n, err
		}
	}
}
