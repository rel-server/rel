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
	"fmt"
	"log/slog"
	"reflect"
	"sort"
	"sync/atomic"
	"time"
)

// Profile sets how long and how often things are measured.
type Profile struct {
	Name         string
	Warmup       time.Duration
	Measure      time.Duration
	Repeats      int
	Clients      []int // reads
	WriteClients []int
	WriteOps     int
}

// Profiles are selectable with -profile.
var Profiles = map[string]Profile{
	"quick": {"quick", 2 * time.Second, 5 * time.Second, 1, []int{1, 8}, []int{1, 8}, 200},
	"full":  {"full", 5 * time.Second, 15 * time.Second, 3, []int{1, 8, 32, 64}, []int{1, 8, 32}, 2000},
}

// Config is everything one benchmark run needs.
type Config struct {
	Profile   Profile
	Sizes     []string
	Servers   []Server
	Scenarios []Scenario
	Stack     *Stack
	Rebuild   bool // rebuild the golden databases
}

// Level is the outcome for one client count : the median of the repeats by
// throughput, plus the spread.
type Level struct {
	LoadResult
	RPSMin, RPSMax float64
	Resources      ResourceUse
	CheckError     string // write state check, empty when it passed
}

// Cell is the outcome of one scenario on one server.
type Cell struct {
	Support Support
	Note    string
	// Skipped says why no timing exists : unsupported, output mismatch, error.
	Skipped string
	// Mismatch notes an output difference that the scenario allowed.
	Mismatch string
	Levels   []Level
}

// SizeResult holds Cells[scenario][server] for one data size.
type SizeResult struct {
	Size  string
	Cells map[string]map[Server]*Cell
}

// Result is a whole run.
type Result struct {
	Profile Profile
	Started time.Time
	CPUs    Resources
	Sizes   []*SizeResult
}

// Run executes the benchmark. Servers run strictly one at a time : each is
// started, measured on every scenario, then stopped before the next starts.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	st := cfg.Stack
	if err := st.UpDB(ctx); err != nil {
		return nil, err
	}
	out := &Result{Profile: cfg.Profile, Started: time.Now(), CPUs: st.Res}
	for _, size := range cfg.Sizes {
		slog.Info("preparing data", "size", size)
		if err := st.EnsureGolden(ctx, size, cfg.Rebuild); err != nil {
			return nil, err
		}
		sr := &SizeResult{Size: size, Cells: map[string]map[Server]*Cell{}}
		for _, sc := range cfg.Scenarios {
			sr.Cells[sc.Name] = map[Server]*Cell{}
		}
		out.Sizes = append(out.Sizes, sr)
		reference := map[string]any{} // scenario -> normalized output of the first server that supports it

		for _, srv := range cfg.Servers {
			if err := benchServer(ctx, cfg, size, srv, sr, reference); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

func benchServer(ctx context.Context, cfg Config, size string, srv Server, sr *SizeResult, reference map[string]any) error {
	st := cfg.Stack
	log := slog.With("size", size, "server", srv)
	if err := st.Reset(ctx, size); err != nil {
		return err
	}
	admin, err := st.Pool(ctx, BenchDB)
	if err != nil {
		return err
	}
	env, err := LoadEnv(ctx, admin, size)
	admin.Close()
	if err != nil {
		return err
	}
	if err := st.Start(srv); err != nil {
		return err
	}
	defer func() { _ = st.Stop(srv) }()
	c := NewClient(URL(srv))
	defer c.Close()
	// reads first, on the pristine copy ; writes reset the database per run
	ordered := append([]Scenario(nil), cfg.Scenarios...)
	sort.SliceStable(ordered, func(i, j int) bool { return !ordered[i].Write && ordered[j].Write })

	for _, sc := range ordered {
		impl, has := sc.Impls[srv]
		cell := &Cell{Support: impl.Support, Note: impl.Note}
		sr.Cells[sc.Name][srv] = cell
		if !has || impl.Support == Unsupported {
			cell.Support, cell.Skipped = Unsupported, "unsupported"
			continue
		}
		log.Info("scenario", "name", sc.Name, "support", impl.Support)
		if !sc.Write {
			if reason, note := checkEquivalent(c, env, sc, srv, impl, reference); reason != "" {
				cell.Skipped = reason
				log.Warn("skipped", "scenario", sc.Name, "why", reason)
				continue
			} else {
				cell.Mismatch = note
			}
			for _, clients := range cfg.Profile.Clients {
				lvl := measure(ctx, c, env, impl, srv, cfg.Profile, clients, nil)
				cell.Levels = append(cell.Levels, lvl)
			}
			continue
		}
		// write scenario : fresh database for every (clients, repeat)
		for _, clients := range cfg.Profile.WriteClients {
			lvl, err := measureWrite(ctx, cfg, size, srv, sc, impl, clients)
			if err != nil {
				return err
			}
			cell.Levels = append(cell.Levels, lvl)
		}
		// leave a clean, started server for whatever comes next
		_ = st.Stop(srv)
	}
	return nil
}

// measure runs spec.Repeats times and keeps the median by throughput.
// after, if set, runs once after each repeat (write state checks).
func measure(ctx context.Context, c *Client, env *Env, impl Impl, srv Server, p Profile, clients int, after func(ok int) error) Level {
	var nextN atomic.Uint64
	nextN.Store(1 << 20) // keep clear of the n = 0 used by the equivalence check
	spec := LoadSpec{Clients: clients, Warmup: p.Warmup, Measure: p.Measure}
	if after != nil {
		spec = LoadSpec{Clients: clients, Ops: p.WriteOps}
	}
	var runs []Level
	for range p.Repeats {
		smp := StartSampler(srv)
		r := RunLoad(ctx, c, env, impl, &nextN, spec)
		lvl := Level{LoadResult: r, Resources: smp.Stop()}
		if after != nil {
			if err := after(int(r.Ops)); err != nil {
				lvl.CheckError = err.Error()
			}
		}
		runs = append(runs, lvl)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].RPS < runs[j].RPS })
	med := runs[len(runs)/2]
	med.RPSMin, med.RPSMax = runs[0].RPS, runs[len(runs)-1].RPS
	return med
}

func measureWrite(ctx context.Context, cfg Config, size string, srv Server, sc Scenario, impl Impl, clients int) (Level, error) {
	st := cfg.Stack
	var runs []Level
	for range cfg.Profile.Repeats {
		_ = st.Stop(srv)
		if err := st.Reset(ctx, size); err != nil {
			return Level{}, err
		}
		db, err := st.Pool(ctx, BenchDB)
		if err != nil {
			return Level{}, err
		}
		env, err := LoadEnv(ctx, db, size)
		if err != nil {
			db.Close()
			return Level{}, err
		}
		if err := st.Start(srv); err != nil {
			db.Close()
			return Level{}, err
		}
		c := NewClient(URL(srv))
		one := cfg.Profile
		one.Repeats = 1
		lvl := measure(ctx, c, env, impl, srv, one, clients, func(ok int) error { return sc.Check(ctx, db, ok) })
		c.Close()
		db.Close()
		runs = append(runs, lvl)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].RPS < runs[j].RPS })
	med := runs[len(runs)/2]
	med.RPSMin, med.RPSMax = runs[0].RPS, runs[len(runs)-1].RPS
	for _, r := range runs {
		if r.CheckError != "" {
			med.CheckError = r.CheckError
		}
	}
	return med, nil
}

// checkEquivalent runs the scenario once (n = 0) and compares its normalized
// output with the reference from the first server that supports it. It
// returns a non-empty skip reason on failure, or a note when a mismatch is allowed.
func checkEquivalent(c *Client, env *Env, sc Scenario, srv Server, impl Impl, reference map[string]any) (skip, note string) {
	body, err := impl.Run(c, env, 0)
	if err != nil {
		return "equivalence run failed: " + err.Error(), ""
	}
	if impl.Normalize == nil {
		return "no normalizer", ""
	}
	got, err := impl.Normalize(body)
	if err != nil {
		return "normalizing output failed: " + err.Error(), ""
	}
	ref, have := reference[sc.Name]
	if !have {
		reference[sc.Name] = got
		return "", ""
	}
	if diff := Diff(ref, got); diff != "" {
		if why, ok := sc.AllowMismatch[srv]; ok {
			return "", "output differs (allowed: " + why + "): " + diff
		}
		return "output differs from reference: " + diff, ""
	}
	return "", ""
}

// Diff returns "" when a and b hold the same JSON-like data, else a short
// description of the first difference. Numbers compare as float64.
func Diff(a, b any) string { return diff("$", a, b) }

func diff(path string, a, b any) string {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: object vs %T", path, b)
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		names := make([]string, 0, len(keys))
		for k := range keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, k := range names {
			x, okx := av[k]
			y, oky := bv[k]
			if !okx || !oky {
				return fmt.Sprintf("%s.%s: present in only one side", path, k)
			}
			if d := diff(path+"."+k, x, y); d != "" {
				return d
			}
		}
		return ""
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return fmt.Sprintf("%s: array vs %T", path, b)
		}
		if len(av) != len(bv) {
			return fmt.Sprintf("%s: length %d vs %d", path, len(av), len(bv))
		}
		for i := range av {
			if d := diff(fmt.Sprintf("%s[%d]", path, i), av[i], bv[i]); d != "" {
				return d
			}
		}
		return ""
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Sprintf("%s: %v vs %v", path, a, b)
	}
	return ""
}
