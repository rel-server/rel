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
	"fmt"
	"strings"

	"github.com/samber/oops"
)

// Resources is the CPU split between the three roles that share the machine :
// the load generator, Postgres and the server under test. Each role gets its
// own cores so none starves another (benchmarks/README.md ## Fairness).
type Resources struct {
	LoadCPUs   []int
	DBCPUs     []int
	ServerCPUs []int
}

// PlanResources splits ncpu cores (numbered 0..ncpu-1) : a quarter to the load
// generator, the rest split 40 % Postgres and 60 % server, rounded so every role
// gets at least one core. It errors below 3 cores, where no honest split exists.
func PlanResources(ncpu int) (Resources, error) {
	if ncpu < 3 {
		return Resources{}, oops.With("ncpu", ncpu).Errorf("need at least 3 CPU cores to keep load generator, database and server apart")
	}
	load := max(1, ncpu/4)
	rest := ncpu - load
	db := max(1, rest*2/5)
	r := Resources{}
	i := 0
	for ; i < load; i++ {
		r.LoadCPUs = append(r.LoadCPUs, i)
	}
	for ; i < load+db; i++ {
		r.DBCPUs = append(r.DBCPUs, i)
	}
	for ; i < ncpu; i++ {
		r.ServerCPUs = append(r.ServerCPUs, i)
	}
	return r, nil
}

// CPUSet formats cores the way docker's cpuset and taskset expect : "0,1,2".
func CPUSet(cpus []int) string {
	parts := make([]string, len(cpus))
	for i, c := range cpus {
		parts[i] = fmt.Sprint(c)
	}
	return strings.Join(parts, ",")
}
