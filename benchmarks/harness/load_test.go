package harness

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunLoad(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Query().Get("fail") != "" {
			http.Error(w, "boom", 500)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	c := NewClient(srv.URL)
	defer c.Close()

	ok := Impl{Support: Native, Run: func(c *Client, _ *Env, _ uint64) ([]byte, error) {
		return c.Do(Request{Method: "GET", Path: "/"})
	}}
	var n atomic.Uint64

	t.Run("fixed ops", func(t *testing.T) {
		hits.Store(0)
		r := RunLoad(context.Background(), c, &Env{}, ok, &n, LoadSpec{Clients: 4, Ops: 50})
		if r.Ops != 50 || r.Errors != 0 || hits.Load() != 50 {
			t.Fatalf("ops=%d errors=%d hits=%d", r.Ops, r.Errors, hits.Load())
		}
		if r.TripsPerOp != 1 || r.BytesPerOp != float64(len(`{"ok":true}`)) {
			t.Errorf("trips/op=%v bytes/op=%v", r.TripsPerOp, r.BytesPerOp)
		}
		if r.P50 <= 0 || r.P99 < r.P50 {
			t.Errorf("bad percentiles %+v", r)
		}
	})

	t.Run("duration", func(t *testing.T) {
		r := RunLoad(context.Background(), c, &Env{}, ok, &n, LoadSpec{Clients: 2, Warmup: 50 * time.Millisecond, Measure: 200 * time.Millisecond})
		if r.Ops == 0 || r.RPS <= 0 || r.Elapsed < 200*time.Millisecond {
			t.Errorf("unexpected %+v", r)
		}
	})

	t.Run("errors are counted not timed", func(t *testing.T) {
		bad := Impl{Support: Native, Run: func(c *Client, _ *Env, _ uint64) ([]byte, error) {
			return c.Do(Request{Method: "GET", Path: "/?fail=1"})
		}}
		r := RunLoad(context.Background(), c, &Env{}, bad, &n, LoadSpec{Clients: 2, Ops: 10})
		if r.Ops != 0 || r.Errors != 10 || r.FirstError == "" {
			t.Errorf("unexpected %+v", r)
		}
	})
}

func TestDiff(t *testing.T) {
	a := map[string]any{"x": []any{1.0, map[string]any{"y": "z"}}}
	if d := Diff(a, map[string]any{"x": []any{1.0, map[string]any{"y": "z"}}}); d != "" {
		t.Errorf("equal values differ: %s", d)
	}
	for name, b := range map[string]any{
		"value":  map[string]any{"x": []any{1.0, map[string]any{"y": "w"}}},
		"length": map[string]any{"x": []any{1.0}},
		"key":    map[string]any{"q": 1.0},
		"type":   []any{},
	} {
		if Diff(a, b) == "" {
			t.Errorf("%s difference not detected", name)
		}
	}
}
