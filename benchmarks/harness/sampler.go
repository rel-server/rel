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
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ResourceUse is what docker reported for the server and database containers
// while a measurement ran.
type ResourceUse struct {
	ServerCPU   float64 // average, in percent of one core
	DBCPU       float64
	ServerMemMB float64 // peak
	DBMemMB     float64
	Samples     int
}

// Sampler polls `docker stats` for a server container and the database
// container until Stop is called.
type Sampler struct {
	server, db string
	cancel     context.CancelFunc
	ctx        context.Context
	stop       chan struct{}
	done       sync.WaitGroup
	mu         sync.Mutex
	res        ResourceUse
	srvSum     float64
	dbSum      float64
}

func containerName(service string) string { return "relbench-" + service + "-1" }

// StartSampler begins sampling in the background.
func StartSampler(srv Server) *Sampler {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Sampler{server: containerName(string(srv)), db: containerName("db"), ctx: ctx, cancel: cancel, stop: make(chan struct{})}
	s.done.Add(1)
	go func() {
		defer s.done.Done()
		for {
			select {
			case <-s.stop:
				return
			default:
			}
			s.sample() // blocks ~1-2 s : docker computes CPU over its own window
		}
	}()
	return s
}

// Stop ends sampling and returns the averages of the samples completed so far ;
// a sample still in flight is abandoned, not waited for (each takes about a second).
func (s *Sampler) Stop() ResourceUse {
	close(s.stop)
	s.cancel()
	s.done.Wait()
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.res
	if r.Samples > 0 {
		r.ServerCPU = s.srvSum / float64(r.Samples)
		r.DBCPU = s.dbSum / float64(r.Samples)
	}
	return r
}

func (s *Sampler) sample() {
	out, err := exec.CommandContext(s.ctx, "docker", "stats", "--no-stream", "--format", "{{.Name}} {{.CPUPerc}} {{.MemUsage}}", s.server, s.db).Output()
	if err != nil {
		time.Sleep(500 * time.Millisecond)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	got := 0
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Fields(line)
		if len(f) < 3 {
			continue
		}
		cpu, _ := strconv.ParseFloat(strings.TrimSuffix(f[1], "%"), 64)
		mem := parseMiB(f[2])
		if f[0] == s.server {
			s.srvSum += cpu
			s.res.ServerMemMB = max(s.res.ServerMemMB, mem)
			got++
		} else if f[0] == s.db {
			s.dbSum += cpu
			s.res.DBMemMB = max(s.res.DBMemMB, mem)
			got++
		}
	}
	if got == 2 {
		s.res.Samples++
	}
}

// parseMiB converts docker's "12.5MiB" / "1.2GiB" / "800kB" to mebibytes.
func parseMiB(v string) float64 {
	// ordered : "B" is a suffix of every other unit
	for _, u := range []struct {
		suffix string
		mul    float64
	}{{"GiB", 1024}, {"MiB", 1}, {"KiB", 1.0 / 1024}, {"kB", 1.0 / 1024}, {"B", 1.0 / (1024 * 1024)}} {
		if num, ok := strings.CutSuffix(v, u.suffix); ok {
			f, _ := strconv.ParseFloat(num, 64)
			return f * u.mul
		}
	}
	return 0
}
