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
	"io"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	waitFirstDelay = 500 * time.Millisecond
	waitMaxDelay   = 5 * time.Second
	waitAttemptMax = 5 * time.Second
)

// WaitReady blocks until a connection to uri succeeds and answers a ping,
// retrying for at most budget while Postgres looks temporarily unavailable :
// network failures (refused, DNS, timeout, reset) and SQLSTATE 57P03 ("the
// database system is starting up"). Any other error — bad credentials,
// unknown database, malformed URI — is returned at once, as is ctx's own
// cancellation. A budget <= 0 makes exactly one attempt.
//
// Each attempt is capped at 5 seconds and the last one is clipped to the
// remaining budget. Every failed attempt that will be retried is logged as a
// warning. The returned error is the last attempt's. It opens and closes its
// own connection and leaves no state behind.
func WaitReady(ctx context.Context, uri string, budget time.Duration) error {
	return waitReady(ctx, budget, time.Sleep, func(ctx context.Context) error {
		conn, err := pgx.Connect(ctx, uri)
		if err != nil {
			return err
		}
		defer conn.Close(context.Background())
		return conn.Ping(ctx)
	})
}

// waitReady is WaitReady's loop, with the attempt and the sleep injected so
// the schedule can be tested without a database or real delays.
func waitReady(ctx context.Context, budget time.Duration, sleep func(time.Duration), attempt func(context.Context) error) error {
	deadline := time.Now().Add(budget)
	delay := waitFirstDelay
	for n := 1; ; n++ {
		timeout := waitAttemptMax
		if remaining := time.Until(deadline); remaining < timeout && remaining > 0 {
			timeout = remaining
		}
		actx, cancel := context.WithTimeout(ctx, timeout)
		err := attempt(actx)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil || !isTransientConnectError(err) || !time.Now().Add(delay).Before(deadline) {
			return err
		}
		log.Warn("postgres not ready, retrying", "attempt", n, "retry_in", delay.String(), "error", err.Error())
		sleep(delay)
		delay = min(delay*2, waitMaxDelay)
	}
}

// isTransientConnectError reports whether err means "Postgres may become
// reachable shortly". An allowlist : an unknown error fails fast.
func isTransientConnectError(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "57P03"
	}
	// A URI or config problem never heals by waiting.
	var parseErr *pgconn.ParseConfigError
	if errors.As(err, &parseErr) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.DeadlineExceeded)
}
