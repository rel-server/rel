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

// Package reloadcmd runs the dmut step then reload.cmd, for both startup and reload. Callers decide what a
// failure means.
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

	"github.com/ceymard/dmut/v3/mutations"
	"github.com/google/shlex"
	"github.com/samber/oops"

	"github.com/rel-server/rel/config"
)

// Run runs the dmut step, then interpolates and runs cfg.Reload.Cmd. Both are always attempted ; err joins both
// failures, timeouts included. ran is false when neither is configured. logger must carry no "module" attribute :
// Run adds its own.
func Run(ctx context.Context, cfg *config.Config, logger *slog.Logger) (ran bool, err error) {
	dmutRan, dmutErr := runDmut(ctx, cfg, logger.With("module", "dmut"))
	cmdRan, cmdErr := runCmd(ctx, cfg, logger.With("module", "reloadcmd"))
	return dmutRan || cmdRan, errors.Join(dmutErr, cmdErr)
}

// ErrorAttrs returns a Run failure as slog arguments, including the failing statement's context dmut attaches,
// which err.Error() drops.
func ErrorAttrs(err error) []any {
	return mutations.ErrorAttrs(err)
}

// runCmd interpolates and runs cfg.Reload.Cmd. ran is false when it is empty.
func runCmd(ctx context.Context, cfg *config.Config, logger *slog.Logger) (ran bool, err error) {
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
	// The full parent environment, for Docker-style configuration.
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

// placeholderRe matches a `{name}`/`{name:...:default}` placeholder ; its content may not contain "{" or "}".
var placeholderRe = regexp.MustCompile(`\{([^{}]*)\}`)

// interpolate resolves every `{name1:name2:...:default}` placeholder in s : names are tried in order and the first
// non-empty one wins, else the last segment is a literal default. A placeholder with no default whose name is
// unset is an error, so reload.cmd never runs with an empty substitution.
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

// toString stringifies a config value (always a string, bool or number) as it would be written in the config.
func toString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// logLines logs each line of r tagged with stream, merging a JSON-object line's keys as attributes.
func logLines(r io.Reader, logger *slog.Logger, stream string) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		attrs := []any{"stream", stream}
		var obj map[string]any
		if json.Unmarshal([]byte(line), &obj) == nil {
			for k, v := range obj {
				attrs = append(attrs, k, v)
			}
		}
		logger.Info(line, attrs...)
	}
}
