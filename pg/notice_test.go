package pg

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

// captureLogs routes slog.Default() (which pg's logging.For logger resolves
// on every call) into a buffer for the duration of the test.
func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

const raiseAll = `do $$ begin
  raise warning 'w-msg';
  raise notice 'n-msg';
  raise info 'i-msg';
  raise debug 'd-msg';
end $$`

func TestNoticeLevel_FiltersBySeverityOnBothPools(t *testing.T) {
	cases := []struct {
		level string
		want  []string
		not   []string
	}{
		{"off", nil, []string{"w-msg", "n-msg", "i-msg"}},
		{"", nil, []string{"w-msg"}},
		{"warning", []string{"w-msg", "level=WARN"}, []string{"n-msg", "i-msg"}},
		{"notice", []string{"w-msg", "n-msg", "severity=NOTICE", "sqlstate="}, []string{"i-msg"}},
		{"info", []string{"w-msg", "n-msg", "i-msg"}, nil},
	}
	for _, c := range cases {
		t.Run("level="+c.level, func(t *testing.T) {
			buf := captureLogs(t)
			db, err := NewInfosAdminQuery(testDbURI, testDbURI, 1, "", WithNoticeLevel(c.level))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Pool.Close()
			if _, err := db.Pool.Exec(context.Background(), raiseAll); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("missing %q in:\n%s", w, out)
				}
			}
			for _, n := range c.not {
				if strings.Contains(out, n) {
					t.Errorf("unexpected %q in:\n%s", n, out)
				}
			}
		})
	}
}

func TestNoticeLevel_InvalidIsAnError(t *testing.T) {
	if _, err := NewInfosAdminQuery(testDbURI, testDbURI, 1, "", WithNoticeLevel("loud")); err == nil {
		t.Fatal("expected an error for an unknown level")
	}
}
