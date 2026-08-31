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

package boot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func handlerReturning(body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	})
}

func TestReloadableHandler_FastRequestNotCancelled(t *testing.T) {
	var observedErr error
	h := NewReloadableHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observedErr = r.Context().Err()
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/rel", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if observedErr != nil {
		t.Errorf("expected a finished request's context to have no error, got %v", observedErr)
	}

	// A drain with nothing in flight must return immediately, well under
	// the timeout.
	start := time.Now()
	h.BeginMaintenance()
	h.Drain(5 * time.Second)
	if time.Since(start) > time.Second {
		t.Errorf("expected Drain to return immediately with nothing in flight, took %v", time.Since(start))
	}
}

// TestReloadableHandler_SlowRequestObservesCancelOnTimeout covers
// specs/migrations.md ## Reloading step 2 : a request still running past the
// drain timeout must have its context cancelled.
func TestReloadableHandler_SlowRequestObservesCancelOnTimeout(t *testing.T) {
	started := make(chan struct{})
	var observedErr error
	var mu sync.Mutex

	h := NewReloadableHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
		mu.Lock()
		observedErr = r.Context().Err()
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))

	go func() {
		req := httptest.NewRequest("GET", "/rel", nil)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
	}()

	<-started
	h.BeginMaintenance()
	h.Drain(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if observedErr != context.Canceled {
		t.Errorf("expected the straggler's context to observe context.Canceled, got %v", observedErr)
	}
}

// TestReloadableHandler_MaintenanceRejectsNewRequestsWithout503Reaching
// covers step 1 : a brand-new request during maintenance gets 503 and never
// reaches the inner handler at all.
func TestReloadableHandler_MaintenanceRejectsNewRequestsWithoutReachingInner(t *testing.T) {
	var called int32
	h := NewReloadableHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&called, 1)
	}))
	h.BeginMaintenance()

	req := httptest.NewRequest("GET", "/rel", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 during maintenance, got %d", rec.Code)
	}
	if atomic.LoadInt32(&called) != 0 {
		t.Errorf("expected the inner handler to never be called during maintenance")
	}
}

// TestReloadableHandler_SwapAndEndMaintenanceResumeAgainstNewHandler covers
// steps 6/7 : after Swap + EndMaintenance, requests are served by the NEW
// handler, not the old one.
func TestReloadableHandler_SwapAndEndMaintenanceResumeAgainstNewHandler(t *testing.T) {
	h := NewReloadableHandler(handlerReturning("old"))
	h.BeginMaintenance()
	h.Drain(time.Second)
	h.Swap(handlerReturning("new"))
	h.EndMaintenance()

	req := httptest.NewRequest("GET", "/rel", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 after resuming, got %d", rec.Code)
	}
	if rec.Body.String() != "new" {
		t.Errorf("expected the NEW handler to serve the request, got %q", rec.Body.String())
	}
}

// TestReloadableHandler_EndMaintenanceWithoutSwapResumesOldHandler covers
// EndMaintenance's own doc comment : without a Swap call (e.g. dmut failed
// and reload aborted before step 6), resuming serves the ORIGINAL handler.
func TestReloadableHandler_EndMaintenanceWithoutSwapResumesOldHandler(t *testing.T) {
	h := NewReloadableHandler(handlerReturning("original"))
	h.BeginMaintenance()
	h.Drain(time.Second)
	h.EndMaintenance()

	req := httptest.NewRequest("GET", "/rel", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Body.String() != "original" {
		t.Errorf("expected the original handler to still serve, got %q", rec.Body.String())
	}
}

// TestReloadableHandler_ConcurrentRequestsDuringDrain exercises many
// concurrent in-flight requests, all tracked and all cancelled correctly on
// a drain timeout — run with -race.
func TestReloadableHandler_ConcurrentRequestsDuringDrain(t *testing.T) {
	const n = 50
	var startedWg sync.WaitGroup
	var finishedWg sync.WaitGroup
	startedWg.Add(n)
	finishedWg.Add(n)

	var cancelledCount int32

	h := NewReloadableHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		startedWg.Done()
		<-r.Context().Done()
		if r.Context().Err() == context.Canceled {
			atomic.AddInt32(&cancelledCount, 1)
		}
		finishedWg.Done()
		w.WriteHeader(http.StatusOK)
	}))

	for i := 0; i < n; i++ {
		go func() {
			req := httptest.NewRequest("GET", "/rel", nil)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
		}()
	}

	startedWg.Wait()
	h.BeginMaintenance()
	h.Drain(50 * time.Millisecond)
	finishedWg.Wait()

	if atomic.LoadInt32(&cancelledCount) != n {
		t.Errorf("expected all %d in-flight requests cancelled, got %d", n, cancelledCount)
	}
}
