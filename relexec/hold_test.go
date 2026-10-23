package relexec

import (
	"bytes"
	"errors"
	"testing"
)

func TestHoldWriter_HoldsUpToTheLimitUntilFlushed(t *testing.T) {
	var out bytes.Buffer
	h := newHoldWriter(&out, 8)
	defer h.Close()

	_, _ = h.Write([]byte("1234"))
	_, _ = h.Write([]byte("5678"))
	if out.Len() != 0 || h.released {
		t.Fatalf("expected 8 bytes to be held, got %q released=%v", out.String(), h.released)
	}
	if err := h.Flush(); err != nil {
		t.Fatal(err)
	}
	if out.String() != "12345678" || !h.released {
		t.Fatalf("expected the held bytes after Flush, got %q released=%v", out.String(), h.released)
	}
}

func TestHoldWriter_PassesEverythingOnOnceTheLimitIsExceeded(t *testing.T) {
	var out bytes.Buffer
	h := newHoldWriter(&out, 4)
	defer h.Close()

	_, _ = h.Write([]byte("123"))
	_, _ = h.Write([]byte("45")) // 5 > 4 : held bytes go first, in order
	if out.String() != "12345" || !h.released {
		t.Fatalf("expected held and new bytes in order, got %q released=%v", out.String(), h.released)
	}
	_, _ = h.Write([]byte("6"))
	if out.String() != "123456" {
		t.Fatalf("expected pass-through after release, got %q", out.String())
	}
}

func TestHoldWriter_ZeroLimitHoldsNothing(t *testing.T) {
	var out bytes.Buffer
	h := newHoldWriter(&out, 0)
	defer h.Close()

	_, _ = h.Write([]byte("a"))
	if out.String() != "a" || !h.released {
		t.Fatalf("expected the first byte to pass on at once, got %q released=%v", out.String(), h.released)
	}
}

func TestHoldWriter_NothingReachesTheWriterWhenNotFlushed(t *testing.T) {
	var out bytes.Buffer
	h := newHoldWriter(&out, 16)
	_, _ = h.Write([]byte("abc"))
	h.Close()
	if out.Len() != 0 || h.released {
		t.Fatalf("expected nothing written, got %q released=%v", out.String(), h.released)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("gone") }

func TestHoldWriter_ReportsTheWriterFailureOnFlush(t *testing.T) {
	h := newHoldWriter(failingWriter{}, 8)
	defer h.Close()
	_, _ = h.Write([]byte("abc"))
	if err := h.Flush(); err == nil {
		t.Fatal("expected the writer's error")
	}
}
