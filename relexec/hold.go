package relexec

import (
	"io"
	"sync"
)

// holdWriter holds back the first limit bytes, so a failure in a small response can still be answered with an
// error. Past limit it passes everything on ; a limit of 0 holds nothing.
type holdWriter struct {
	w     io.Writer
	buf   []byte
	limit int
	// released is true once bytes have been passed to w.
	released bool
}

var holdPool sync.Pool // of *[]byte

func newHoldWriter(w io.Writer, limit int) *holdWriter {
	h := &holdWriter{w: w, limit: limit}
	if limit > 0 {
		if b, ok := holdPool.Get().(*[]byte); ok && cap(*b) >= limit {
			h.buf = (*b)[:0]
		} else {
			h.buf = make([]byte, 0, limit)
		}
	}
	return h
}

func (h *holdWriter) Write(p []byte) (int, error) {
	if h.released {
		return h.w.Write(p)
	}
	if len(h.buf)+len(p) <= h.limit {
		h.buf = append(h.buf, p...)
		return len(p), nil
	}
	if err := h.release(); err != nil {
		return 0, err
	}
	return h.w.Write(p)
}

// release passes the held bytes on.
func (h *holdWriter) release() error {
	h.released = true
	if len(h.buf) == 0 {
		return nil
	}
	_, err := h.w.Write(h.buf)
	h.buf = h.buf[:0]
	return err
}

// Flush passes on what is still held. The response is complete once it returns nil.
func (h *holdWriter) Flush() error {
	if h.released {
		return nil
	}
	return h.release()
}

// Close gives the held-bytes buffer back for reuse; the writer must not be used afterwards.
func (h *holdWriter) Close() {
	if cap(h.buf) > 0 {
		b := h.buf[:0]
		holdPool.Put(&b)
	}
	h.buf = nil
}
