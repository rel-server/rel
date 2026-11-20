package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestLoad_TypeScriptTypes(t *testing.T) {
	t.Chdir(t.TempDir())
	p := writeFile(t, t.TempDir(), "rel.toml", `
[typescript.types]
"public.doc.body" = "Body"
"public.doc" = { title = "string" }

[typescript.types.app.users.settings]
type = "Settings"
from = "./settings"
comment = "user settings"
`)
	t.Setenv("REL_TYPESCRIPT__TYPES__APP__USERS__PREFS", "Prefs")
	cfg, err := Load([]string{"--config=" + p})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := map[string]TypeDecl{
		"public.doc.body":    {Type: "Body"},
		"public.doc.title":   {Type: "string"},
		"app.users.settings": {Type: "Settings", From: "./settings", Comment: "user settings"},
		"app.users.prefs":    {Type: "Prefs"},
	}
	if !reflect.DeepEqual(cfg.TypeScript.Types, want) {
		t.Errorf("typescript.types : got %#v", cfg.TypeScript.Types)
	}
}

func TestLoad_TypeScriptTypes_Malformed(t *testing.T) {
	for name, src := range map[string]string{
		"too shallow":   `typescript.types.public.doc = "Body"`,
		"too deep":      "[typescript.types]\n\"public.doc.body.x\" = \"Body\"",
		"unknown field": "[typescript.types.public.doc.body]\ntipe = \"Body\"",
		"not a string":  "[typescript.types.public.doc.body]\ntype = 3",
	} {
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			p := writeFile(t, t.TempDir(), "rel.toml", src)
			if _, err := Load([]string{"--config=" + p}); err == nil || !strings.Contains(err.Error(), "typescript.types") {
				t.Errorf("want a typescript.types error, got %v", err)
			}
		})
	}
}
