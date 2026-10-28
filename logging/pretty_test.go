package logging

import (
	"bytes"
	"log/slog"
	"regexp"
	"strings"
	"testing"
)

var ansiSeq = regexp.MustCompile("\x1b\\[[0-9;]*m")

func prettyLogger(color bool) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(newPrettyHandler(&buf, slog.LevelDebug, color)), &buf
}

// TestPretty_AccessLineShowsVerbAndPathAsMessage : the access line has no "request" message nor
// method/path attributes, they are its "GET /x" head instead.
func TestPretty_AccessLineShowsVerbAndPathAsMessage(t *testing.T) {
	l, buf := prettyLogger(false)
	l.Info(accessLogMessage, "path", "/x", "method", "GET", "role", "admin", "status", 200)

	out := buf.String()
	if !strings.Contains(out, " INF GET /x role=admin status=200\n") {
		t.Errorf("unexpected access line: %q", out)
	}
	if strings.Contains(out, "request") || strings.Contains(out, "method=") || strings.Contains(out, "path=") {
		t.Errorf("expected no message and no method/path attributes: %q", out)
	}
}

// TestPretty_AccessMessageWithoutVerbAndPathIsPlain : the special form needs both attributes.
func TestPretty_AccessMessageWithoutVerbAndPathIsPlain(t *testing.T) {
	l, buf := prettyLogger(false)
	l.Info(accessLogMessage, "path", "/x")
	if out := buf.String(); !strings.Contains(out, "INF request path=/x\n") {
		t.Errorf("unexpected line: %q", out)
	}
}

// TestPretty_NoColorHasNoEscapeSequence : color off writes plain text.
func TestPretty_NoColorHasNoEscapeSequence(t *testing.T) {
	l, buf := prettyLogger(false)
	l.Error("boom", "status", 500, "role", "r", "path", "/x")
	if strings.Contains(buf.String(), "\x1b") {
		t.Errorf("expected no escape sequence: %q", buf.String())
	}
}

// TestPretty_Colors : each part of the access line gets the color the palette assigns it, and
// 4xx/5xx statuses are bold.
func TestPretty_Colors(t *testing.T) {
	cases := []struct {
		name   string
		attrs  []any
		wants  []string
		reject []string
	}{
		{"GET 200", []any{"path", "/x", "method", "GET", "role", "r", "status", 200},
			[]string{"\x1b[1m\x1b[38;5;104mGET\x1b[0m", "\x1b[38;5;30m/x\x1b[0m", "\x1b[38;5;169mr\x1b[0m", "\x1b[38;5;40m200\x1b[0m"}, []string{"\x1b[1m\x1b[38;5;40m"}},
		{"POST 404", []any{"path", "/x", "method", "POST", "role", "r", "status", 404},
			[]string{"\x1b[38;5;202mPOST", "\x1b[1m\x1b[38;5;100m404\x1b[0m"}, nil},
		{"DELETE 503", []any{"path", "/x", "method", "DELETE", "role", "r", "status", 503},
			[]string{"\x1b[38;5;243mDELETE", "\x1b[1m\x1b[38;5;124m503\x1b[0m"}, nil},
		{"302 uncolored", []any{"path", "/x", "method", "GET", "role", "r", "status", 302},
			[]string{"\x1b[0m302\n"}, []string{"\x1b[38;5;40m302", "\x1b[38;5;100m302", "\x1b[38;5;124m302"}},
	}
	for _, c := range cases {
		l, buf := prettyLogger(true)
		l.Info(accessLogMessage, c.attrs...)
		out := buf.String()
		for _, w := range c.wants {
			if !strings.Contains(out, w) {
				t.Errorf("%s: missing %q in %q", c.name, w, out)
			}
		}
		for _, w := range c.reject {
			if strings.Contains(out, w) {
				t.Errorf("%s: unexpected %q in %q", c.name, w, out)
			}
		}
	}
}

// TestPretty_QuotesAndEscapes : a value that could break the line or inject a terminal escape
// is quoted.
func TestPretty_QuotesAndEscapes(t *testing.T) {
	l, buf := prettyLogger(false)
	l.Info("m", "a", "has space", "b", "x=y", "c", "\x1b[31mred", "d", "", "e", "plain")
	out := buf.String()
	for _, w := range []string{`a="has space"`, `b="x=y"`, `c="\x1b[31mred"`, `d=""`, "e=plain"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in %q", w, out)
		}
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("raw escape in output: %q", out)
	}
}

// TestPretty_WithAttrsAndGroups : With attributes come before the call's own, groups flatten
// into dotted keys, and the level filters.
func TestPretty_WithAttrsAndGroups(t *testing.T) {
	var buf bytes.Buffer
	l := slog.New(newPrettyHandler(&buf, slog.LevelInfo, false))
	l.Debug("hidden")
	l.With("module", "pg").WithGroup("g").Info("m", "k", 1, slog.Group("sub", "z", true))
	out := ansiSeq.ReplaceAllString(buf.String(), "")
	if strings.Contains(out, "hidden") {
		t.Errorf("debug line should be filtered: %q", out)
	}
	if !strings.Contains(out, " INF m module=pg g.k=1 g.sub.z=true\n") {
		t.Errorf("unexpected line: %q", out)
	}
}

// TestColorEnabled_HonorsNoColor : only a non-empty NO_COLOR turns color off.
func TestColorEnabled_HonorsNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if !colorEnabled() {
		t.Error("expected color on when NO_COLOR is empty")
	}
	t.Setenv("NO_COLOR", "1")
	if colorEnabled() {
		t.Error("expected color off when NO_COLOR=1")
	}
}

// TestPretty_AccessLineKeepsRequestIDOnlyAtDebug : request_id is left off the access line unless
// the handler is at debug level, where the request's other lines exist to be correlated. Other
// lines always keep it.
func TestPretty_AccessLineKeepsRequestIDOnlyAtDebug(t *testing.T) {
	for _, c := range []struct {
		level slog.Level
		want  bool
	}{{slog.LevelInfo, false}, {slog.LevelDebug, true}} {
		var buf bytes.Buffer
		l := slog.New(requestIDHandler{inner: newPrettyHandler(&buf, c.level, false), id: "abc"})
		l.Info(accessLogMessage, "path", "/x", "method", "GET", "status", 200)
		l.Info("other line")
		lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
		if got := strings.Contains(lines[0], "request_id=abc"); got != c.want {
			t.Errorf("level %v: access line has request_id = %v, want %v: %q", c.level, got, c.want, lines[0])
		}
		if !strings.Contains(lines[1], "request_id=abc") {
			t.Errorf("level %v: other line lost its request_id: %q", c.level, lines[1])
		}
	}
}

// TestPretty_ResponseSizeIsHumanReadable : the pretty output shows response_size with a unit,
// the JSON output keeps the byte count.
func TestPretty_ResponseSizeIsHumanReadable(t *testing.T) {
	for n, want := range map[int]string{0: "0B", 512: "512B", 1023: "1023B", 1024: "1KiB", 1536: "1.5KiB", 2 << 20: "2MiB", 5<<30 + 1<<29: "5.5GiB"} {
		l, buf := prettyLogger(false)
		l.Info("m", "response_size", n)
		if want := "response_size=" + want + "\n"; !strings.HasSuffix(buf.String(), want) {
			t.Errorf("%d: want suffix %q, got %q", n, want, buf.String())
		}
	}
}

// TestPretty_AccessLineLeadsWithTargetsAndWrite : a /rel line shows targets then write right after
// the verb and path, colored, write by its value ; without targets both stay in place.
func TestPretty_AccessLineLeadsWithTargetsAndWrite(t *testing.T) {
	l, buf := prettyLogger(false)
	l.Info(accessLogMessage, "path", "/rel", "method", "POST", "role", "r", "status", 200,
		"item_count", 2, "write", true, "targets", []string{"public.film", "public.actor"})
	if want := " INF POST /rel targets=public.film,public.actor write=true role=r status=200 item_count=2\n"; !strings.HasSuffix(buf.String(), want) {
		t.Errorf("want suffix %q, got %q", want, buf.String())
	}

	l, buf = prettyLogger(false)
	l.Info(accessLogMessage, "path", "/rel", "method", "POST", "status", 401, "item_count", 0, "write", false, "targets", []string(nil))
	if want := " INF POST /rel status=401 item_count=0 write=false\n"; !strings.HasSuffix(buf.String(), want) {
		t.Errorf("want suffix %q, got %q", want, buf.String())
	}

	l, buf = prettyLogger(true)
	l.Info(accessLogMessage, "path", "/rel", "method", "POST", "write", true, "targets", []string{"a"})
	l.Info(accessLogMessage, "path", "/rel", "method", "POST", "write", false, "targets", []string{"a"})
	out := buf.String()
	for _, w := range []string{"\x1b[38;5;39ma\x1b[0m", "\x1b[38;5;100mtrue\x1b[0m", "\x1b[38;5;40mfalse\x1b[0m"} {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in %q", w, out)
		}
	}
}
