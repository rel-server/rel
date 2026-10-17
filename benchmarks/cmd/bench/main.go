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

// Command bench runs the cross-server benchmark. See benchmarks/README.md.
//
//	go run ./benchmarks/cmd/bench -profile quick -sizes small
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/rel-server/rel/benchmarks/harness"
	"github.com/rel-server/rel/benchmarks/scenarios"
	"golang.org/x/sys/unix"
)

func main() {
	var (
		profile   = flag.String("profile", "quick", "quick or full")
		sizes     = flag.String("sizes", "", "comma-separated data sizes (small, medium, large) ; default small for quick, all for full")
		srvs      = flag.String("servers", "rel,postgrest,postgraphile4,postgraphile5", "comma-separated servers")
		only      = flag.String("scenarios", "", "regexp ; only scenarios whose name matches")
		out       = flag.String("out", "", "results directory ; default benchmarks/results/<date>-<profile>")
		check     = flag.Bool("check", false, "run each scenario once per server without timing (equivalence and write smoke test)")
		rebuild   = flag.Bool("rebuild", false, "rebuild the golden databases")
		verbose   = flag.Bool("v", false, "stream docker compose output")
		dir       = flag.String("dir", "benchmarks", "the benchmarks directory (where compose.yaml is)")
		noPin     = flag.Bool("no-pin", false, "do not pin the load generator to its CPUs")
		listScens = flag.Bool("list", false, "list scenarios and exit")
	)
	flag.Parse()

	p, ok := harness.Profiles[*profile]
	if !ok {
		fatal("unknown profile %q", *profile)
	}
	all := scenarios.All()
	if *listScens {
		for _, s := range all {
			fmt.Println(s.Name)
		}
		return
	}
	if *only != "" {
		re, err := regexp.Compile(*only)
		if err != nil {
			fatal("bad -scenarios: %v", err)
		}
		var keep []harness.Scenario
		for _, s := range all {
			if re.MatchString(s.Name) {
				keep = append(keep, s)
			}
		}
		all = keep
	}
	sz := strings.Split(*sizes, ",")
	if *sizes == "" {
		sz = []string{"small"}
		if *profile == "full" {
			sz = []string{"small", "medium", "large"}
		}
	}
	var servers []harness.Server
	for _, s := range strings.Split(*srvs, ",") {
		servers = append(servers, harness.Server(s))
	}

	res, err := harness.PlanResources(runtime.NumCPU())
	if err != nil {
		fatal("%v", err)
	}
	if !*noPin {
		var set unix.CPUSet
		for _, c := range res.LoadCPUs {
			set.Set(c)
		}
		if err := unix.SchedSetaffinity(0, &set); err != nil {
			fatal("pinning load generator: %v", err)
		}
		runtime.GOMAXPROCS(len(res.LoadCPUs))
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	stack := &harness.Stack{Dir: *dir, Res: res, Verbose: *verbose}
	defer func() { _ = stack.Down() }()

	result, err := harness.Run(ctx, harness.Config{
		Profile: p, Sizes: sz, Servers: servers, Scenarios: all,
		Stack: stack, Rebuild: *rebuild, CheckOnly: *check,
	})
	if err != nil {
		_ = stack.Down()
		fatal("%v", err)
	}
	dest := *out
	if dest == "" {
		dest = filepath.Join(*dir, "results", time.Now().Format("2006-01-02T1504")+"-"+*profile)
	}
	if err := harness.WriteReport(dest, result, stack); err != nil {
		fatal("%v", err)
	}
	slog.Info("report written", "dir", dest)
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "bench: "+format+"\n", args...)
	os.Exit(1)
}
