package wellknown

import "testing"

func TestResolveParams_RequiredMissing_Errors(t *testing.T) {
	c := &Compiled{Params: map[string]ParamDef{
		"name": {Type: "text"},
	}}
	if _, err := c.ResolveParams(nil); err == nil {
		t.Fatalf("expected an error for a missing required param")
	}
}

func TestResolveParams_DefaultAppliedWhenAbsent(t *testing.T) {
	c := &Compiled{Params: map[string]ParamDef{
		"limit": {Type: "int", HasDefault: true, Default: []byte("10")},
	}}
	out, err := c.ResolveParams(nil)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if v, ok := out["limit"].(float64); !ok || v != 10 {
		t.Fatalf("expected limit=10, got %#v", out["limit"])
	}
}

func TestResolveParams_NullDefaultAppliedWhenAbsent(t *testing.T) {
	c := &Compiled{Params: map[string]ParamDef{
		"tag": {Type: "text", HasDefault: true, Default: []byte("null")},
	}}
	out, err := c.ResolveParams(nil)
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if v, ok := out["tag"]; !ok || v != nil {
		t.Fatalf("expected tag=nil (present, null), got %#v (present=%v)", v, ok)
	}
}

func TestResolveParams_SuppliedValueOverridesDefault(t *testing.T) {
	c := &Compiled{Params: map[string]ParamDef{
		"limit": {Type: "int", HasDefault: true, Default: []byte("10")},
	}}
	out, err := c.ResolveParams([]byte(`{"limit": 25}`))
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if v, ok := out["limit"].(float64); !ok || v != 25 {
		t.Fatalf("expected limit=25, got %#v", out["limit"])
	}
}

func TestResolveParams_TypeMismatch_Errors(t *testing.T) {
	c := &Compiled{Params: map[string]ParamDef{
		"limit": {Type: "int"},
	}}
	if _, err := c.ResolveParams([]byte(`{"limit": "not a number"}`)); err == nil {
		t.Fatalf("expected a type mismatch error for a string where int was declared")
	}
}

func TestResolveParams_NullValueBypassesTypeCheck(t *testing.T) {
	c := &Compiled{Params: map[string]ParamDef{
		"limit": {Type: "int"},
	}}
	out, err := c.ResolveParams([]byte(`{"limit": null}`))
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if v, ok := out["limit"]; !ok || v != nil {
		t.Fatalf("expected limit=nil (present, null), got %#v (present=%v)", v, ok)
	}
}

func TestResolveParams_UnrecognizedExtraKeyIgnored(t *testing.T) {
	c := &Compiled{Params: map[string]ParamDef{
		"limit": {Type: "int", HasDefault: true, Default: []byte("10")},
	}}
	out, err := c.ResolveParams([]byte(`{"limit": 5, "extra": "ignored"}`))
	if err != nil {
		t.Fatalf("ResolveParams: %v", err)
	}
	if _, ok := out["extra"]; ok {
		t.Fatalf("expected an undeclared extra key to be silently ignored, not returned")
	}
}

func TestCheckParamType_Buckets(t *testing.T) {
	cases := []struct {
		pgType  string
		json    string
		wantErr bool
	}{
		{"int", "5", false},
		{"int", `"5"`, true},
		{"text", `"hello"`, false},
		{"text", "5", true},
		{"boolean", "true", false},
		{"boolean", `"true"`, true},
		{"jsonb", `{"a":1}`, false},
		{"", `[1,2,3]`, false},
		{"uuid", `"00000000-0000-0000-0000-000000000000"`, false},
	}
	for _, c := range cases {
		err := checkParamType(c.pgType, []byte(c.json))
		if (err != nil) != c.wantErr {
			t.Errorf("checkParamType(%q, %s): got err=%v, wantErr=%v", c.pgType, c.json, err, c.wantErr)
		}
	}
}
