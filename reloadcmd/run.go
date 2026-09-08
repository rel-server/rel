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

// Package reloadcmd runs reload.cmd (specs/reload.md), the single entry
// point both cmd/rel's startup sequence and boot's reload sequence call in
// place of the migration tool rel used to embed directly. This package does
// not decide the "log and continue" policy on failure itself — both call
// sites apply that policy identically, in exactly one place each, rather
// than duplicating it here.
package reloadcmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/google/shlex"
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
)

// Run interpolates and runs cfg.Reload.Cmd, per specs/reload.md. ran is
// false when cfg.Reload.Cmd is empty — nothing to run, not an error ; err is
// non-nil when interpolation fails, the command line can't be parsed/
// started, it exits non-zero, or cfg.Reload.Timeout elapses first.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) (ran bool, err error) {
	if cfg.Reload.Cmd == "" {
		return false, nil
	}

	interpolated, ierr := interpolate(cfg.Reload.Cmd, cfg.Raw)
	if ierr != nil {
		return true, oops.Wrapf(ierr, "interpolating reload.cmd")
	}

	args, serr := shlex.Split(interpolated)
	if serr != nil {
		return true, oops.Wrapf(serr, "parsing reload.cmd")
	}
	if len(args) == 0 {
		return true, oops.Errorf("reload.cmd resolved to an empty command line")
	}

	timeout := time.Duration(cfg.Reload.Timeout) * time.Second
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(runCtx, args[0], args[1:]...)
	// The full parent environment, unmodified — specs/reload.md : "this
	// allows for Docker-style configuration mechanisms that rely on the
	// ambient environment" — on top of whatever interpolate already
	// substituted into the command line itself.
	cmd.Env = os.Environ()

	stdout, perr := cmd.StdoutPipe()
	if perr != nil {
		return true, oops.Wrapf(perr, "piping reload.cmd stdout")
	}
	stderr, perr := cmd.StderrPipe()
	if perr != nil {
		return true, oops.Wrapf(perr, "piping reload.cmd stderr")
	}

	if serr := cmd.Start(); serr != nil {
		return true, oops.Wrapf(serr, "starting reload.cmd")
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); logLines(stdout, logger, "stdout") }()
	go func() { defer wg.Done(); logLines(stderr, logger, "stderr") }()
	wg.Wait()

	if werr := cmd.Wait(); werr != nil {
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			return true, oops.Errorf("reload.cmd timed out after %s", timeout)
		}
		return true, oops.Wrapf(werr, "reload.cmd exited with an error")
	}

	return true, nil
}

// placeholderRe matches specs/dmut.md's `{name}`/`{name:...:default}`
// interpolation syntax : brace-delimited content, split into `:`-separated
// segments by interpolate itself. Content may not contain "{" or "}".
var placeholderRe = regexp.MustCompile(`\{([^{}]*)\}`)

// interpolate resolves every `{name}`/`{name1:name2:...:default}` placeholder
// in s against raw (a dotted configuration key, when a name contains ".") or
// the process environment (otherwise), per specs/dmut.md. All but the last
// `:`-separated segment are names tried in order ; the first that resolves
// to a non-empty value wins. The last segment is always a literal default,
// never resolved as a name. A single-segment placeholder (no default) whose
// name is unset is an error — aborting before reload.cmd ever runs, rather
// than silently substituting an empty string.
func interpolate(s string, raw map[string]any) (string, error) {
	var firstErr error
	result := placeholderRe.ReplaceAllStringFunc(s, func(match string) string {
		if firstErr != nil {
			return match
		}
		sub := placeholderRe.FindStringSubmatch(match)
		segments := strings.Split(sub[1], ":")

		if len(segments) == 1 {
			name := segments[0]
			if val, ok := resolve(name, raw); ok {
				return val
			}
			firstErr = oops.Errorf("%s: not set, and no default given", name)
			return match
		}

		candidates, literal := segments[:len(segments)-1], segments[len(segments)-1]
		for _, name := range candidates {
			if val, ok := resolve(name, raw); ok && val != "" {
				return val
			}
		}
		return literal
	})
	if firstErr != nil {
		return "", firstErr
	}
	return result, nil
}

// resolve looks name up as a configuration key (raw, dotted) when it
// contains a ".", otherwise as an environment variable.
func resolve(name string, raw map[string]any) (string, bool) {
	if strings.Contains(name, ".") {
		v, ok := raw[name]
		if !ok {
			return "", false
		}
		return toString(v), true
	}
	return os.LookupEnv(name)
}

// toString stringifies a resolved config value the way its config file/
// flag/env-var representation would already have looked (koanf.All()'s
// values are string/bool/int/float64 — never anything requiring %v-style
// struct formatting).
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// logLines scans r line by line, tagging each with component=reload.cmd
// and stream, merging a JSON-object line's own keys on top when it parses
// as one — specs/reload.md : "for commands that support structured output."
// A bufio.Scanner, not a naive per-Write split : os/exec pipes output
// through io.Copy in arbitrary chunks, never guaranteed to align with line
// boundaries the way a single formatted log call would.
func logLines(r io.Reader, logger *slog.Logger, stream string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		attrs := []any{"component", "reload.cmd", "stream", stream}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) == nil {
			for k, v := range obj {
				attrs = append(attrs, k, v)
			}
		}
		logger.Info(line, attrs...)
	}
}
