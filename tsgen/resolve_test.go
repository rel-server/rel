package tsgen

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rel-server/rel/config"
)

func resolveCfg(schemas, codec string) *config.Config {
	cfg := config.Test()
	cfg.TypeScript.Schemas, cfg.TypeScript.Codec = schemas, codec
	return cfg
}

func TestResolve_Schemas(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		override   string
		want       []string
		badRequest bool
	}{
		{"no whitelist, no override", "", "", nil, false},
		{"no whitelist, override passes through", "", "a,b", []string{"a", "b"}, false},
		{"whitelist, no override", "a,b", "", []string{"a", "b"}, false},
		{"whitelist, override narrows", "a,b", "a", []string{"a"}, false},
		{"whitelist, spaces and blanks are ignored", " a , b,", " b ,", []string{"b"}, false},
		{"whitelist, override outside it is rejected", "a,b", "a,c", nil, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := Resolve(resolveCfg(c.configured, ""), Overrides{Schemas: c.override})
			if c.badRequest {
				var bad *BadOverrideError
				if !errors.As(err, &bad) || !strings.Contains(err.Error(), `"c"`) {
					t.Fatalf("expected a BadOverrideError naming the schema, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(opts.Schemas, c.want) {
				t.Fatalf("got %v, want %v", opts.Schemas, c.want)
			}
		})
	}
}

func TestResolve_Codec(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		override   string
		want       string
	}{
		{"neither", "", "", ""},
		{"configured", "cbor", "", "cbor,date,bigint"},
		{"override replaces configured, no merge", "cbor", "json", "json"},
		{"blank override falls back", "cbor", "  ", "cbor,date,bigint"},
		{"override alone", "", "temporal", "json,temporal"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts, err := Resolve(resolveCfg("", c.configured), Overrides{Codec: c.override})
			if err != nil {
				t.Fatal(err)
			}
			want, err := ParseCodec(c.want)
			if err != nil {
				t.Fatalf("test expectation %q: %v", c.want, err)
			}
			if opts.Codec != want {
				t.Fatalf("got %+v, want %+v", opts.Codec, want)
			}
		})
	}
}

func TestResolve_WhoIsToBlame(t *testing.T) {
	_, err := Resolve(resolveCfg("", ""), Overrides{Codec: "msgpack"})
	var bad *BadOverrideError
	if !errors.As(err, &bad) {
		t.Errorf("a bad codec override is the caller's mistake, got %v", err)
	}

	_, err = Resolve(resolveCfg("", "msgpack"), Overrides{})
	if err == nil || errors.As(err, &bad) {
		t.Errorf("a bad configured codec is not the caller's mistake, got %v", err)
	}
	// ... and an override does not hide it : the configured value is only read when it is used.
	if _, err := Resolve(resolveCfg("", "msgpack"), Overrides{Codec: "json"}); err != nil {
		t.Errorf("an override replaces the configured codec, so it is not parsed: %v", err)
	}
}

func TestResolve_CarriesTheBlacklist(t *testing.T) {
	cfg := resolveCfg("", "")
	opts, err := Resolve(cfg, Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(opts.Blacklist, cfg.Blacklist) {
		t.Errorf("got %+v, want %+v", opts.Blacklist, cfg.Blacklist)
	}
}
