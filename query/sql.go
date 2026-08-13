package query

import "bytes"

// This is a stub. It will need to be suplemented / implemented.

type SqlQuery struct {
	buf bytes.Buffer
}

func (q *SqlQuery) SurroundWith(start, end string, fn func()) {
	q.buf.WriteString(start)
	fn()
	q.buf.WriteString(end)
}

// writes and returns a PostgreSQL escaped string
func (q *SqlQuery) EscapeId(id string) string {
	return ""
}

func (q *SqlQuery) Append(str ...string) {
	for _, s := range str {
		q.buf.WriteString(s)
	}
}
