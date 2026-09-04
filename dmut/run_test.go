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

package dmut

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/ceymard/rel/config"
)

func testLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

// TestRun_MissingDirectorySkipsSilently : ## Execution — a nonexistent
// dmut.path is not an error, skipped and logged at info level.
func TestRun_MissingDirectorySkipsSilently(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(&buf)

	ran, err := Run(context.Background(), "postgres://doesnotmatter/db", config.Dmut{
		Path: filepath.Join(t.TempDir(), "does-not-exist"),
	}, logger)
	if err != nil {
		t.Fatalf("expected no error for a missing dmut.path, got %v", err)
	}
	if ran {
		t.Fatalf("expected ran=false for a missing dmut.path")
	}
	if !bytes.Contains(buf.Bytes(), []byte("dmut.path does not exist")) {
		t.Errorf("expected an info log line about the missing path, got %q", buf.String())
	}
}

// TestLogWriter_ForwardsLineByLine : each Write call becomes one
// logger.Info call, trailing newline stripped rather than embedded.
func TestLogWriter_ForwardsLineByLine(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(&buf)
	w := &logWriter{logger: logger}

	n, err := w.Write([]byte("applying mutation foo\n"))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len("applying mutation foo\n") {
		t.Errorf("expected Write to report the full byte count, got %d", n)
	}

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("applying mutation foo")) {
		t.Errorf("expected the line forwarded into the logger, got %q", out)
	}
	if !bytes.Contains([]byte(out), []byte("component=dmut")) {
		t.Errorf("expected component=dmut attribute, got %q", out)
	}
	if bytes.Count([]byte(out), []byte("applying mutation foo\n\n")) > 0 {
		t.Errorf("expected the trailing newline stripped before logging, got %q", out)
	}
}

// TestLogWriter_EmptyLineIsNotLogged : a bare "\n" Write call shouldn't
// produce an empty log record.
func TestLogWriter_EmptyLineIsNotLogged(t *testing.T) {
	var buf bytes.Buffer
	logger := testLogger(&buf)
	w := &logWriter{logger: logger}

	if _, err := w.Write([]byte("\n")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("expected no log record for an empty line, got %q", buf.String())
	}
}
