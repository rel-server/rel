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

package harness

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/HdrHistogram/hdrhistogram-go"
)

// LoadSpec describes one measurement : Clients goroutines, each sending its
// next operation as soon as the previous one returns (closed loop).
//
// With Ops == 0 the run lasts Warmup (discarded) then Measure. With Ops > 0 it
// performs exactly Ops operations, no warm-up : writes use this so every
// server sees the same amount of data change.
type LoadSpec struct {
	Clients int
	Warmup  time.Duration
	Measure time.Duration
	Ops     int
}

// LoadResult is what one LoadSpec measured.
type LoadResult struct {
	Clients    int
	Ops        int64 // successful operations
	Errors     int64
	FirstError string
	Elapsed    time.Duration
	RPS        float64 // successful operations per second
	P50, P95   time.Duration
	P99, Max   time.Duration
	// BytesPerOp and TripsPerOp are averages over the measured window.
	BytesPerOp float64
	TripsPerOp float64
}

const (
	histMin = int64(time.Microsecond)
	histMax = int64(time.Minute)
)

// RunLoad drives impl with spec.Clients concurrent clients. nextN is shared
// across calls so successive runs never reuse an operation number.
func RunLoad(ctx context.Context, c *Client, env *Env, impl Impl, nextN *atomic.Uint64, spec LoadSpec) LoadResult {
	if spec.Ops == 0 && spec.Warmup > 0 {
		runWindow(ctx, c, env, impl, nextN, spec.Clients, spec.Warmup, 0)
	}
	trips0, bytes0 := c.Trips(), c.Bytes()
	res := runWindow(ctx, c, env, impl, nextN, spec.Clients, spec.Measure, spec.Ops)
	res.Clients = spec.Clients
	if res.Ops > 0 {
		res.BytesPerOp = float64(c.Bytes()-bytes0) / float64(res.Ops+res.Errors)
		res.TripsPerOp = float64(c.Trips()-trips0) / float64(res.Ops+res.Errors)
	}
	return res
}

func runWindow(ctx context.Context, c *Client, env *Env, impl Impl, nextN *atomic.Uint64, clients int, d time.Duration, fixedOps int) LoadResult {
	var (
		wg       sync.WaitGroup
		errs     atomic.Int64
		issued   atomic.Int64
		firstErr atomic.Value
		hists    = make([]*hdrhistogram.Histogram, clients)
		stopAt   = time.Now().Add(d)
	)
	start := time.Now()
	for i := range clients {
		hists[i] = hdrhistogram.New(histMin, histMax, 3)
		wg.Add(1)
		go func(h *hdrhistogram.Histogram) {
			defer wg.Done()
			for ctx.Err() == nil {
				if fixedOps > 0 {
					if issued.Add(1) > int64(fixedOps) {
						return
					}
				} else if !time.Now().Before(stopAt) {
					return
				}
				n := nextN.Add(1)
				// Building the request (marshalling, and rel-cbor's conversion to CBOR) is the load
				// generator's work, not the server's : done before the clock starts, so only sending and
				// receiving are timed. An operation of several requests is built and timed whole.
				pre, prepared := c.Prepare(impl, env, n)
				t0 := time.Now()
				var err error
				if prepared {
					_, err = c.Send(pre)
				} else {
					_, err = impl.Run(c, env, n)
				}
				if err != nil {
					errs.Add(1)
					firstErr.CompareAndSwap(nil, err.Error())
					continue
				}
				_ = h.RecordValue(max(histMin, min(histMax, int64(time.Since(t0)))))
			}
		}(hists[i])
	}
	wg.Wait()
	elapsed := time.Since(start)

	all := hdrhistogram.New(histMin, histMax, 3)
	for _, h := range hists {
		all.Merge(h)
	}
	res := LoadResult{
		Ops:     all.TotalCount(),
		Errors:  errs.Load(),
		Elapsed: elapsed,
		P50:     time.Duration(all.ValueAtQuantile(50)),
		P95:     time.Duration(all.ValueAtQuantile(95)),
		P99:     time.Duration(all.ValueAtQuantile(99)),
		Max:     time.Duration(all.Max()),
	}
	if s, ok := firstErr.Load().(string); ok {
		res.FirstError = s
	}
	if elapsed > 0 {
		res.RPS = float64(res.Ops) / elapsed.Seconds()
	}
	return res
}
