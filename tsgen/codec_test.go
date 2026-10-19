package tsgen

import (
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
)

func TestParseCodec(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Codec
		str  string
	}{
		{"", Codec{Wire: "json"}, "json"},
		{"json", Codec{Wire: "json"}, "json"},
		{" temporal , bigint ", Codec{Wire: "json", Time: "temporal", BigInt: true}, "json,temporal,bigint"},
		{"bigint,date", Codec{Wire: "json", Time: "date", BigInt: true}, "json,date,bigint"},
		{"cbor", Codec{Wire: "cbor", Time: "date", BigInt: true}, "cbor,date,bigint"},
		{"temporal,cbor", Codec{Wire: "cbor", Time: "temporal", BigInt: true}, "cbor,temporal,bigint"},
		{",,json,", Codec{Wire: "json"}, "json"},
	} {
		got, err := ParseCodec(tc.in)
		if err != nil {
			t.Errorf("ParseCodec(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseCodec(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if got.String() != tc.str {
			t.Errorf("ParseCodec(%q).String() = %q, want %q", tc.in, got.String(), tc.str)
		}
		if back, err := ParseCodec(got.String()); err != nil || back != got {
			t.Errorf("ParseCodec(%q.String()) = %+v, %v ; want %+v", tc.in, back, err, got)
		}
	}
}

func TestParseCodec_Errors(t *testing.T) {
	for in, want := range map[string]string{
		"json,cbor":          "both set the wire format",
		"date,temporal":      "both set the date and time type",
		"cbor,cbor":          "twice",
		"bigint,bigint":      "twice",
		"msgpack":            "unknown token",
		"Temporal":           "unknown token", // tokens are lower case
		"cbor,date,temporal": "both set the date and time type",
	} {
		_, err := ParseCodec(in)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseCodec(%q) error = %v, want one containing %q", in, err, want)
		}
	}
}

// allCodecs is every valid combination, canonical.
func allCodecs() []Codec {
	var out []Codec
	for _, wire := range []string{"json", "cbor"} {
		for _, tm := range []string{"", "date", "temporal"} {
			for _, big := range []bool{false, true} {
				c := Codec{Wire: wire, Time: tm, BigInt: big}.normalized()
				dup := false
				for _, o := range out {
					dup = dup || o == c
				}
				if !dup {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// TestGenerateDatabaseTS_Codecs type-checks the file of every codec combination, and that a combination that needs
// no Temporal type compiles against a lib without it.
func TestGenerateDatabaseTS_Codecs(t *testing.T) {
	for _, c := range allCodecs() {
		t.Run(c.String(), func(t *testing.T) {
			out := GenerateDatabaseTS(testDb, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist(), Codec: c}, nil)
			if !strings.Contains(out, "// Codec: "+c.String()+"\n") {
				t.Errorf("missing the codec header ; starts with:\n%.300s", out)
			}
			lib := `"ESNext", "dom"`
			if c.Time != "temporal" {
				lib = `"ES2022", "dom"`
			}
			assertTypeChecksWithLib(t, out, lib)
		})
	}
}

// TestGenerateDatabaseTS_CodecImports : the plain file has no import, and a package is imported only by the codec
// that needs it, at the top and once.
func TestGenerateDatabaseTS_CodecImports(t *testing.T) {
	gen := func(s string) string {
		c, err := ParseCodec(s)
		if err != nil {
			t.Fatal(err)
		}
		return GenerateDatabaseTS(testDb, Options{Schemas: []string{"hotel"}, Blacklist: config.DefaultBlacklist(), Codec: c}, nil)
	}
	for _, s := range []string{"", "json", "date", "temporal"} {
		if out := gen(s); strings.Contains(out, "\nimport ") || strings.Contains(out, "from \"") {
			t.Errorf("codec %q: expected no import", s)
		}
	}
	if out := gen("bigint"); strings.Count(out, `from "lossless-json"`) != 1 || strings.Contains(out, `from "cbor-x"`) {
		t.Errorf("bigint: expected exactly the lossless-json import")
	}
	out := gen("cbor,temporal")
	if strings.Count(out, `from "cbor-x"`) != 1 || strings.Contains(out, `from "lossless-json"`) {
		t.Errorf("cbor,temporal: expected exactly the cbor-x import")
	}
	if !strings.Contains(out, "export interface PgWire {\n  int8: number | bigint\n  bytea: Uint8Array\n  timestamptz: Temporal.Instant") {
		t.Errorf("cbor,temporal: unexpected PgWire block")
	}
	if strings.Contains(gen("cbor"), "Temporal.Instant\n") {
		t.Errorf("cbor: the instant must be a Date")
	}
}
