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

// Package boot houses the maintenance/reload mechanics that sit above
// server+rpc, per specs/03-dmut.md ## Reloading : a permanent, reload-aware
// http.Handler wrapper (ReloadableHandler) that becomes http.Server.Handler
// exactly once, at startup, and the orchestration (RunReload) tying
// together dmut.Run/pg.ReIntrospect/rpc.BuildRegistry/server.NewRelHandler/
// rpc.NewHandler for both the startup path and every subsequent SIGUSR1.
package boot

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// maintenancePageBody is the fixed, plain-text 503 page served for every
// new request while a reload is in progress — specs/03-dmut.md ## Reloading
// step 1 : "a small, fixed convenience page ... no templating."
const maintenancePageBody = "rel is applying migrations, please retry shortly.\n"

// ReloadableHandler is http.Server.Handler's own, permanent value — see
// specs/03-dmut.md ## Reloading's "http.Server.Handler is, permanently...
// a small reload-aware wrapper holding an atomic.Pointer" paragraph. The
// inner *http.Handler is only ever swapped via Swap ; ServeHTTP always
// dispatches through the current pointer, so http.Server.Handler itself
// never needs to change after startup.
type ReloadableHandler struct {
	inner atomic.Pointer[http.Handler]

	// mu guards maintenance and inflight together : a request must be
	// unable to observe "not in maintenance" and then get registered into
	// inflight AFTER BeginMaintenance has already flipped the flag and
	// moved on to draining — checking maintenance and registering into
	// inflight happen under the same lock in ServeHTTP for exactly that
	// reason.
	mu          sync.Mutex
	maintenance bool
	inflight    map[*inflightRequest]struct{}
	wg          sync.WaitGroup
}

// inflightRequest is one in-flight request's own cancel func, individually
// cancellable on drain-timeout — a plain sync.WaitGroup alone can count
// in-flight requests but can't cancel a specific straggler's context, which
// specs/03-dmut.md ## Reloading step 2 requires (pgx honors context
// cancellation and releases whatever locks that request was holding).
type inflightRequest struct {
	cancel context.CancelFunc
}

// NewReloadableHandler wraps initial as the wrapper's starting inner
// handler.
func NewReloadableHandler(initial http.Handler) *ReloadableHandler {
	h := &ReloadableHandler{
		inflight: make(map[*inflightRequest]struct{}),
	}
	h.inner.Store(&initial)
	return h
}

// ServeHTTP either serves the fixed maintenance page (new requests during a
// reload never reach the inner handler at all) or dispatches to the
// current inner handler, tracking this request as in-flight — under its
// own individually-cancellable context — for the duration.
func (h *ReloadableHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	if h.maintenance {
		h.mu.Unlock()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Retry-After", "5")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(maintenancePageBody))
		return
	}

	ctx, cancel := context.WithCancel(r.Context())
	entry := &inflightRequest{cancel: cancel}
	h.inflight[entry] = struct{}{}
	h.wg.Add(1)
	h.mu.Unlock()

	defer func() {
		h.mu.Lock()
		delete(h.inflight, entry)
		h.mu.Unlock()
		h.wg.Done()
		// Always cancel, even on the happy path : avoids leaking the
		// context.WithCancel's own internal goroutine/resources — a
		// no-op if Drain already fired it on timeout.
		cancel()
	}()

	inner := *h.inner.Load()
	inner.ServeHTTP(w, r.WithContext(ctx))
}

// Swap stores next as the current inner handler — a single atomic pointer
// store, never a write to http.Server.Handler itself (specs/03-dmut.md ##
// Reloading step 6).
func (h *ReloadableHandler) Swap(next http.Handler) {
	h.inner.Store(&next)
}

// BeginMaintenance flips into maintenance mode : every NEW request from
// this point gets 503 + the maintenance page. Returns immediately —
// requests already in flight keep running until Drain deals with them.
func (h *ReloadableHandler) BeginMaintenance() {
	h.mu.Lock()
	h.maintenance = true
	h.mu.Unlock()
}

// Drain waits for every request that was already in flight when
// BeginMaintenance was called to finish, up to timeout. Past that, it
// cancels each still-running request's context (specs/03-dmut.md ##
// Reloading step 2) and then waits for them to actually return : a
// cancelled context only asks pgx/handlers to unwind, it doesn't force
// ServeHTTP to return synchronously, and dmut's own DDL needs those
// handlers to have actually released their locks before it starts, not
// merely have been asked to.
func (h *ReloadableHandler) Drain(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		h.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(timeout):
	}

	h.mu.Lock()
	for entry := range h.inflight {
		entry.cancel()
	}
	h.mu.Unlock()

	// Wait (unbounded) for the now-cancelled handlers to actually return
	// and deregister themselves — cancellation is a request, not a
	// guarantee of immediate return, but every handler in this codebase
	// is expected to honor ctx promptly (pgx does), so this is bounded in
	// practice even though it has no second timeout here.
	<-done
}

// EndMaintenance flips back to normal serving against whatever the current
// inner handler is — either the newly Swap-ed one, or the original one if
// the caller never called Swap (e.g. dmut failed and reload aborted before
// reaching step 6).
func (h *ReloadableHandler) EndMaintenance() {
	h.mu.Lock()
	h.maintenance = false
	h.mu.Unlock()
}
