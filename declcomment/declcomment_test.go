package declcomment

import "testing"

func TestExtract(t *testing.T) {
	for _, c := range []struct {
		comment, raw string
		found, err   bool
	}{
		{"a plain comment", "", false, false},
		{"column: see below", "", false, false},
		{"columns:: type: \"X\"", "", false, false}, // "s::" follows "column" : no declaration
		{"  column:: type: \"X\", comment: \"c\"", `{"comment":"c","type":"X"}`, true, false},
		{`column: {"type": "X"}`, `{"type": "X"}`, true, false},
		{"column:: type: ", "", true, true},
	} {
		raw, found, err := Extract(c.comment, "column")
		if found != c.found || (err != nil) != c.err || (!c.err && string(raw) != c.raw) {
			t.Errorf("%q : %s, found %v, err %v", c.comment, raw, found, err)
		}
	}
}

func TestDecode(t *testing.T) {
	for _, c := range []struct {
		raw string
		ok  bool
	}{
		{`{"type": "{ a: number }"}`, true},
		{`{"type": "Settings", "from": "./settings", "comment": "c"}`, true},
		{`{"comment": "only a comment"}`, true},
		{`{"tipe": "X"}`, false},
		{`{"type": "A\nB"}`, false},
		{`{"type": "Settings<number>", "from": "./s"}`, false},
		{`{"from": "./s"}`, false},
		{`{"type": "S", "from": "./s\""}`, false},
		{`[1]`, false},
	} {
		if _, err := Decode([]byte(c.raw)); (err == nil) != c.ok {
			t.Errorf("%s : %v", c.raw, err)
		}
	}
}
