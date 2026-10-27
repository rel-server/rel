package jwt

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestLogValueIntegralTimestamps(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	l.Info("x", "claims", LogValue(Claims{"role": "test", "exp": 1.793113902e9, "ratio": 1.5}))
	out := buf.String()
	for _, want := range []string{"claims.exp=1793113902", "claims.role=test", "claims.ratio=1.5"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}
