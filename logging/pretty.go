package logging

import (
	"context"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// accessLogMessage is the message StartAccessLog logs with. The pretty handler shows such a line
// as "GET /path" in place of the message, and leaves out its method and path attributes.
const accessLogMessage = "request"

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiFaint = "\x1b[2m"
)

// prettyHandler writes one human-readable line per record : time, level, message, then key=value
// attributes. With color off, the line has no escape sequence at all. Safe for concurrent use.
type prettyHandler struct {
	out   io.Writer
	level slog.Leveler
	color bool

	// mu is shared by every handler derived through WithAttrs/WithGroup, since they write to the
	// same out.
	mu *sync.Mutex

	// attrs is the attributes accumulated by WithAttrs, already formatted ; group is the key
	// prefix ("a.b.") that WithGroup built.
	attrs string
	group string
}

func newPrettyHandler(out io.Writer, level slog.Leveler, color bool) *prettyHandler {
	return &prettyHandler{out: out, level: level, color: color, mu: &sync.Mutex{}}
}

func (h *prettyHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level.Level()
}

func (h *prettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var b strings.Builder
	b.WriteString(h.attrs)
	for _, a := range attrs {
		h.writeAttr(&b, h.group, a)
	}
	c := *h
	c.attrs = b.String()
	return &c
}

func (h *prettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	c := *h
	c.group = h.group + name + "."
	return &c
}

func (h *prettyHandler) Handle(_ context.Context, r slog.Record) error {
	var b strings.Builder

	if !r.Time.IsZero() {
		h.paint(&b, r.Time.Format(time.StampMilli), -1, false, true)
		b.WriteByte(' ')
	}
	h.writeLevel(&b, r.Level)
	b.WriteByte(' ')

	// Pulled out of the attributes so the access line can show them as its message.
	var method, path string
	var targets []string
	var write slog.Attr
	isAccess := r.Message == accessLogMessage
	if isAccess {
		r.Attrs(func(a slog.Attr) bool {
			switch a.Key {
			case "method":
				method = a.Value.String()
			case "path":
				path = a.Value.String()
			case "targets":
				targets, _ = a.Value.Any().([]string)
			case "write":
				write = a
			}
			return true
		})
		isAccess = method != "" && path != ""
	}
	// A /rel line says what it touched, and whether it wrote, right after the verb and path ;
	// without targets (a request refused early) both stay where they are.
	leadWithTargets := isAccess && len(targets) > 0

	if isAccess {
		h.paint(&b, method, verbColor(method), true, false)
		b.WriteByte(' ')
		h.paint(&b, path, colorPath, false, false)
	} else if r.Message != "" {
		b.WriteString(r.Message)
	}
	if leadWithTargets {
		h.writeAttr(&b, "", slog.Any("targets", targets))
		if write.Key != "" {
			h.writeAttr(&b, "", write)
		}
	}
	b.WriteString(h.attrs)
	// Below debug the access line is the only line of its request, so its request_id correlates
	// nothing ; it is kept when debug adds the request's other lines.
	hideRequestID := isAccess && h.level.Level() > slog.LevelDebug
	r.Attrs(func(a slog.Attr) bool {
		switch {
		case isAccess && (a.Key == "method" || a.Key == "path"),
			hideRequestID && a.Key == "request_id",
			isAccess && a.Key == "targets", // shown first, or empty
			leadWithTargets && a.Key == "write":
			return true
		}
		h.writeAttr(&b, h.group, a)
		return true
	})
	b.WriteByte('\n')

	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := io.WriteString(h.out, b.String())
	return err
}

// writeLevel writes the three-letter level label, colored.
func (h *prettyHandler) writeLevel(b *strings.Builder, level slog.Level) {
	switch {
	case level < slog.LevelInfo:
		h.paint(b, "DBG", colorLevelDebug, false, false)
	case level < slog.LevelWarn:
		h.paint(b, "INF", colorLevelInfo, false, false)
	case level < slog.LevelError:
		h.paint(b, "WRN", colorLevelWarn, false, false)
	default:
		h.paint(b, "ERR", colorLevelError, true, false)
	}
}

// writeAttr writes " key=value" for a, flattening groups into dotted keys. The path, role and
// status, targets and write values are colored.
func (h *prettyHandler) writeAttr(b *strings.Builder, prefix string, a slog.Attr) {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return
	}
	if a.Value.Kind() == slog.KindGroup {
		if a.Key != "" {
			prefix += a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			h.writeAttr(b, prefix, ga)
		}
		return
	}

	b.WriteByte(' ')
	h.paint(b, prefix+a.Key+"=", -1, false, true)

	text := formatValue(a.Value)
	color, bold := -1, false
	// Only the top-level keys carry the meaning, a dotted one is something else.
	if prefix == "" {
		switch a.Key {
		case "path":
			color = colorPath
		case "role":
			color = colorRole
		case "status":
			if a.Value.Kind() == slog.KindInt64 {
				color, bold = statusColor(int(a.Value.Int64()))
			}
		case "response_size":
			if a.Value.Kind() == slog.KindInt64 {
				text = humanBytes(a.Value.Int64())
			}
		case "targets":
			if ts, ok := a.Value.Any().([]string); ok {
				text, color = formatValue(slog.StringValue(strings.Join(ts, ","))), colorTargets
			}
		case "write":
			if a.Value.Kind() == slog.KindBool {
				color = colorWriteFalse
				if a.Value.Bool() {
					color = colorWriteTrue
				}
			}
		}
	}
	h.paint(b, text, color, bold, false)
}

// paint writes text in the given 256-palette color (-1 for none), bold and/or faint. It writes
// text as is when color is off.
func (h *prettyHandler) paint(b *strings.Builder, text string, color int, bold, faint bool) {
	if !h.color || (color < 0 && !bold && !faint) {
		b.WriteString(text)
		return
	}
	if bold {
		b.WriteString(ansiBold)
	}
	if faint {
		b.WriteString(ansiFaint)
	}
	if color >= 0 {
		b.WriteString("\x1b[38;5;")
		b.WriteString(strconv.Itoa(color))
		b.WriteByte('m')
	}
	b.WriteString(text)
	b.WriteString(ansiReset)
}

// humanBytes renders a byte count with a binary unit and at most one decimal : 512B, 1.5KiB, 2MiB.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return strconv.FormatInt(n, 10) + "B"
	}
	size, suffix := float64(n), ""
	for _, s := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		size /= unit
		suffix = s
		if size < unit {
			break
		}
	}
	return strings.TrimSuffix(strconv.FormatFloat(size, 'f', 1, 64), ".0") + suffix
}

// formatValue renders v, quoting a string that is empty or holds a space, '=', '"' or a control
// character, so a value can neither run into the next attribute nor inject an escape sequence.
func formatValue(v slog.Value) string {
	var s string
	switch v.Kind() {
	case slog.KindString:
		s = v.String()
	case slog.KindTime:
		return v.Time().Format(time.RFC3339Nano)
	default:
		s = v.String()
	}
	if s == "" || strings.ContainsFunc(s, func(r rune) bool {
		return r <= ' ' || r == '=' || r == '"' || r == 0x7f
	}) {
		return strconv.Quote(s)
	}
	return s
}
