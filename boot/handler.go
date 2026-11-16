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

// Package boot holds startup and reload : the permanent reload-aware handler and the sequence that builds and
// swaps snapshots.
package boot

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// maintenancePageBody is the fixed, untemplated 503 page for a reload in progress.
const maintenancePageBody = "rel is applying migrations, please retry shortly.\n"

// ReloadableHandler is the permanent http.Server.Handler ; reloads swap its inner handler. Safe for concurrent use.
type ReloadableHandler struct {
	inner atomic.Pointer[http.Handler]

	// mu guards maintenance and inflight together, or a request could register after draining began.
	mu          sync.Mutex
	maintenance bool
	inflight    map[*inflightRequest]struct{}
	wg          sync.WaitGroup
}

// inflightRequest lets Drain cancel each request on timeout.
type inflightRequest struct {
	cancel context.CancelFunc
}

func NewReloadableHandler(initial http.Handler) *ReloadableHandler {
	h := &ReloadableHandler{
		inflight: make(map[*inflightRequest]struct{}),
	}
	h.inner.Store(&initial)
	return h
}

// ServeHTTP serves the maintenance page during a reload, else the current inner handler under a context Drain can
// cancel.
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
		cancel()
	}()

	inner := *h.inner.Load()
	inner.ServeHTTP(w, r.WithContext(ctx))
}

// Swap makes next the inner handler for every request from now on.
func (h *ReloadableHandler) Swap(next http.Handler) {
	h.inner.Store(&next)
}

// BeginMaintenance makes every new request get the 503 maintenance page. Returns immediately ; in-flight requests
// keep running until Drain.
func (h *ReloadableHandler) BeginMaintenance() {
	h.mu.Lock()
	h.maintenance = true
	h.mu.Unlock()
}

// Drain waits up to timeout for in-flight requests, then cancels their contexts and waits for them to return :
// reload.cmd's DDL needs their locks released.
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

	// Unbounded : every handler is expected to honor ctx promptly.
	<-done
}

// EndMaintenance resumes serving with the current inner handler, swapped or not.
func (h *ReloadableHandler) EndMaintenance() {
	h.mu.Lock()
	h.maintenance = false
	h.mu.Unlock()
}
