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

package reloadcmd

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
)

func TestRun_EmptyCmdSkipsSilently(t *testing.T) {
	cfg := &config.Config{}
	ran, err := Run(context.Background(), cfg, discardLogger())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if ran {
		t.Errorf("expected ran=false for an empty reload.cmd")
	}
}

func TestRun_SucceedsAndLogsStdoutStderr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	cfg := &config.Config{Reload: config.Reload{
		Cmd:     `sh -c "echo out-line; echo err-line 1>&2"`,
		Timeout: 5,
	}}
	ran, err := Run(context.Background(), cfg, logger)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !ran {
		t.Fatalf("expected ran=true")
	}
	out := buf.String()
	if !strings.Contains(out, "out-line") || !strings.Contains(out, `stream=stdout`) {
		t.Errorf("expected stdout line tagged stream=stdout, got %q", out)
	}
	if !strings.Contains(out, "err-line") || !strings.Contains(out, `stream=stderr`) {
		t.Errorf("expected stderr line tagged stream=stderr, got %q", out)
	}
	if !strings.Contains(out, `component=reload.cmd`) {
		t.Errorf("expected component=reload.cmd tag, got %q", out)
	}
}

func TestRun_JSONLineMergesKeysIntoLogRecord(t *testing.T) {
	// The JSON payload travels through an env var, not a literal "{...}"
	// in reload.cmd itself : interpolation runs on the raw command string
	// BEFORE shell-splitting, so a literal brace there would be parsed as
	// a (malformed) placeholder rather than reach the child process as-is.
	t.Setenv("RELOADCMD_TEST_JSON", `{"applied":3}`)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	cfg := &config.Config{Reload: config.Reload{
		Cmd:     `sh -c "echo \"$RELOADCMD_TEST_JSON\""`,
		Timeout: 5,
	}}
	if _, err := Run(context.Background(), cfg, logger); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(buf.String(), "applied=3") {
		t.Errorf("expected the JSON line's own key merged into the log record, got %q", buf.String())
	}
}

func TestRun_NonZeroExitIsAnError(t *testing.T) {
	cfg := &config.Config{Reload: config.Reload{Cmd: "false", Timeout: 5}}
	ran, err := Run(context.Background(), cfg, discardLogger())
	if !ran {
		t.Errorf("expected ran=true even on failure")
	}
	if err == nil {
		t.Errorf("expected a non-zero exit to be an error")
	}
}

func TestRun_TimeoutIsAnError(t *testing.T) {
	cfg := &config.Config{Reload: config.Reload{Cmd: "sleep 5", Timeout: 1}}
	_, err := Run(context.Background(), cfg, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected a timeout error, got %v", err)
	}
}

func TestRun_InterpolatesConfigKeyAndEnvVar(t *testing.T) {
	t.Setenv("RELOADCMD_TEST_VAR", "env-value")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))

	cfg := &config.Config{Reload: config.Reload{
		Cmd:     `echo {pg.uri} {RELOADCMD_TEST_VAR}`,
		Timeout: 5,
	}, Raw: map[string]any{"pg.uri": "postgres://x/y"}}
	if _, err := Run(context.Background(), cfg, logger); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(buf.String(), "postgres://x/y env-value") {
		t.Errorf("expected both placeholders interpolated, got %q", buf.String())
	}
}

func TestRun_UnresolvedPlaceholderWithNoDefaultAbortsBeforeRunning(t *testing.T) {
	cfg := &config.Config{Reload: config.Reload{Cmd: `echo {nope.missing}`, Timeout: 5}}
	ran, err := Run(context.Background(), cfg, discardLogger())
	if err == nil {
		t.Fatalf("expected an error for an unresolved placeholder with no default")
	}
	if !ran {
		t.Errorf("expected ran=true : reload.cmd was non-empty, so an attempt was made, even though it never actually exec'd")
	}
}

func TestInterpolate_DefaultFallback(t *testing.T) {
	got, err := interpolate("host={pg.host:localhost}", nil)
	if err != nil {
		t.Fatalf("interpolate: %v", err)
	}
	if got != "host=localhost" {
		t.Errorf("expected the default value substituted, got %q", got)
	}
}

func TestInterpolate_DottedNameResolvesAsConfigKey(t *testing.T) {
	raw := map[string]any{"pg.port": 5432}
	got, err := interpolate("{pg.port}", raw)
	if err != nil {
		t.Fatalf("interpolate: %v", err)
	}
	if got != "5432" {
		t.Errorf("expected the raw config value stringified, got %q", got)
	}
}

func TestInterpolate_BareNameResolvesAsEnvVar(t *testing.T) {
	t.Setenv("SOME_BARE_VAR", "bare-value")
	got, err := interpolate("{SOME_BARE_VAR}", nil)
	if err != nil {
		t.Fatalf("interpolate: %v", err)
	}
	if got != "bare-value" {
		t.Errorf("expected the env var's value, got %q", got)
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))
}
