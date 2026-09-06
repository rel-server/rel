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

// Package dmut drives github.com/ceymard/dmut/v2's mutations package
// against rel's own configuration, per specs/migrations.md — the single entry
// point both cmd/rel's startup sequence and boot's SIGUSR1 reload sequence
// call. This package does not decide the "log and continue" policy on
// failure itself (see Run's own doc comment) — both call sites apply that
// policy identically, in exactly one place each, rather than duplicating it
// here.
package dmut

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/rel-server/rel/config"
	"github.com/samber/oops"

	dmutlib "github.com/ceymard/dmut/v2/mutations"
)

// Run drives dmut against primaryURI using the mutation files under
// cfg.Path. Returns (ran bool, err error) : ran is false when cfg.Path
// doesn't exist on disk (dmut skipped entirely, logged at info level —
// specs/migrations.md ## Execution's "effectively optional" case) ; err is
// non-nil when the directory exists but the run itself failed (caller
// decides the log-and-continue policy — this function does not swallow the
// error itself, so both the startup and reload call sites can apply that
// policy in exactly one place rather than duplicating it).
func Run(ctx context.Context, primaryURI string, cfg config.Dmut, logger *slog.Logger) (ran bool, err error) {
	if _, statErr := os.Stat(cfg.Path); statErr != nil {
		if os.IsNotExist(statErr) {
			logger.Info("dmut.path does not exist — skipping dmut", "path", cfg.Path)
			return false, nil
		}
		return false, oops.Wrapf(statErr, "checking dmut.path %q", cfg.Path)
	}

	output := &logWriter{logger: logger}

	if err := dmutlib.ReadAndRunMutations(primaryURI, []string{cfg.Path}, &dmutlib.MutationRunnerOptions{
		Commit: true,
		Output: output,
	}); err != nil {
		return true, oops.Wrapf(err, "running dmut mutations under %q", cfg.Path)
	}

	return true, nil
}

// logWriter adapts dmut's *log.Logger output into rel's structured logger :
// each Write call (one newline-terminated line) becomes one logger.Info call.
type logWriter struct {
	logger *slog.Logger
}

func (w *logWriter) Write(p []byte) (int, error) {
	line := strings.TrimRight(string(p), "\n")
	if line != "" {
		w.logger.Info(line, "component", "dmut")
	}
	return len(p), nil
}
