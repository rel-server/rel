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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/samber/oops"
)

// WriteReport saves raw.json and report.md for r in dir.
func WriteReport(dir string, r *Result, stack *Stack) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return oops.Wrapf(err, "creating %s", dir)
	}
	raw, err := sonic.MarshalIndent(r, "", " ")
	if err != nil {
		return oops.Wrapf(err, "encoding results")
	}
	if err := os.WriteFile(filepath.Join(dir, "raw.json"), raw, 0o644); err != nil {
		return oops.Wrapf(err, "writing raw.json")
	}
	if err := os.WriteFile(filepath.Join(dir, "report.md"), []byte(Markdown(r, stack)), 0o644); err != nil {
		return oops.Wrapf(err, "writing report.md")
	}
	return nil
}

func ms(d time.Duration) string { return fmt.Sprintf("%.2f", float64(d.Microseconds())/1000) }

// Markdown renders r : the environment first, then one table per scenario and data size.
func Markdown(r *Result, stack *Stack) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Benchmark report\n\n")
	fmt.Fprintf(&b, "- Started: %s\n- Profile: %s (warm-up %s, measure %s, %d repeat(s), median run reported)\n",
		r.Started.Format(time.RFC3339), r.Profile.Name, r.Profile.Warmup, r.Profile.Measure, r.Profile.Repeats)
	fmt.Fprintf(&b, "- Machine: %d logical CPUs, %s/%s, Go %s\n", runtime.NumCPU(), runtime.GOOS, runtime.GOARCH, runtime.Version())
	fmt.Fprintf(&b, "- CPU pinning: load generator %s, Postgres %s, server under test %s\n",
		CPUSet(r.CPUs.LoadCPUs), CPUSet(r.CPUs.DBCPUs), CPUSet(r.CPUs.ServerCPUs))
	if stack != nil {
		if out, err := exec.Command("docker", "compose", "-f", filepath.Join(stack.Dir, "compose.yaml"), "config", "--images").Output(); err == nil {
			fmt.Fprintf(&b, "- Images: `%s`\n", strings.Join(strings.Fields(string(out)), "`, `"))
		}
	}
	b.WriteString("- Closed-loop load : each client sends its next request when the previous answer arrives, so tail latencies under overload are understated.\n")
	b.WriteString("- Support: **native** = one request ; **emulated** = the client chains requests (round trips listed) ; **unsupported** = skipped.\n\n")

	for _, sr := range r.Sizes {
		names := make([]string, 0, len(sr.Cells))
		for n := range sr.Cells {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(&b, "## Data size: %s\n\n", sr.Size)
		for _, name := range names {
			fmt.Fprintf(&b, "### %s\n\n", name)
			b.WriteString("| Server | Support | Clients | req/s | (min–max) | p50 ms | p95 ms | p99 ms | errors | server CPU % | DB CPU % | server MiB | bytes/op | trips/op |\n")
			b.WriteString("|---|---|---|---|---|---|---|---|---|---|---|---|---|---|\n")
			for _, srv := range AllServers {
				cell := sr.Cells[name][srv]
				if cell == nil {
					continue
				}
				support := cell.Support.String()
				if cell.Note != "" {
					support += " (" + cell.Note + ")"
				}
				if cell.Skipped != "" {
					fmt.Fprintf(&b, "| %s | %s | – | skipped: %s | | | | | | | | | | |\n", srv, support, cell.Skipped)
					continue
				}
				for _, l := range cell.Levels {
					cpu, dbCPU, mem := "–", "–", "–" // not sampled
					if l.Resources.Samples > 0 {
						cpu, dbCPU, mem = fmt.Sprintf("%.0f", l.Resources.ServerCPU), fmt.Sprintf("%.0f", l.Resources.DBCPU), fmt.Sprintf("%.0f", l.Resources.ServerMemMB)
					}
					fmt.Fprintf(&b, "| %s | %s | %d | %.0f | %.0f–%.0f | %s | %s | %s | %d | %s | %s | %s | %.0f | %.1f |\n",
						srv, support, l.Clients, l.RPS, l.RPSMin, l.RPSMax, ms(l.P50), ms(l.P95), ms(l.P99), l.Errors,
						cpu, dbCPU, mem, l.BytesPerOp, l.TripsPerOp)
					if l.FirstError != "" {
						fmt.Fprintf(&b, "| | | | first error: `%s` | | | | | | | | | | |\n", strings.ReplaceAll(l.FirstError, "|", "/"))
					}
					if l.CheckError != "" {
						fmt.Fprintf(&b, "| | | | **state check failed:** %s | | | | | | | | | | |\n", strings.ReplaceAll(l.CheckError, "|", "/"))
					}
				}
				if cell.Mismatch != "" {
					fmt.Fprintf(&b, "| | | | note: %s | | | | | | | | | | |\n", strings.ReplaceAll(cell.Mismatch, "|", "/"))
				}
			}
			b.WriteString("\n")
		}
	}
	return b.String()
}
