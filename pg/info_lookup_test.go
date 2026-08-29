// Copyright 2025 Christophe Eymard
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package pg

import "testing"

func TestSearchPath_IncludesPublic(t *testing.T) {
	found := false
	for _, s := range testDb.SearchPath {
		if s == "public" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected \"public\" in SearchPath, got %v", testDb.SearchPath)
	}
}

// withSearchPath temporarily overrides testDb.SearchPath for the duration of
// one test, restoring it afterward. Tests in this package run sequentially
// (none call t.Parallel()), so mutating the shared testDb is safe here.
func withSearchPath(t *testing.T, path []string, fn func()) {
	t.Helper()
	saved := testDb.SearchPath
	testDb.SearchPath = path
	defer func() { testDb.SearchPath = saved }()
	fn()
}

func TestResolveRelation_ViaSearchPath(t *testing.T) {
	withSearchPath(t, []string{"public"}, func() {
		r := testDb.ResolveRelation("", "director")
		if r == nil || r.Identifier.Schema != "public" {
			t.Fatalf("expected public.director, got %v", r)
		}
	})
}

func TestResolveRelation_AmbiguousSearchPathOrderWins(t *testing.T) {
	withSearchPath(t, []string{"alt_schema", "public"}, func() {
		r := testDb.ResolveRelation("", "director")
		if r == nil || r.Identifier.Schema != "alt_schema" {
			t.Errorf("expected alt_schema.director to win when alt_schema is first, got %v", r)
		}
	})

	withSearchPath(t, []string{"public", "alt_schema"}, func() {
		r := testDb.ResolveRelation("", "director")
		if r == nil || r.Identifier.Schema != "public" {
			t.Errorf("expected public.director to win when public is first, got %v", r)
		}
	})
}

func TestResolveRelation_ExplicitSchemaBypassesSearchPath(t *testing.T) {
	withSearchPath(t, []string{"public"}, func() {
		r := testDb.ResolveRelation("alt_schema", "director")
		if r == nil || r.Identifier.Schema != "alt_schema" {
			t.Errorf("explicit schema should bypass search path entirely, got %v", r)
		}
	})
}

func TestResolveRelation_NotFound(t *testing.T) {
	if r := testDb.ResolveRelation("public", "no_such_relation"); r != nil {
		t.Errorf("expected nil for an unknown relation, got %v", r)
	}
	if r := testDb.ResolveRelation("", "no_such_relation"); r != nil {
		t.Errorf("expected nil for an unknown unqualified relation, got %v", r)
	}
}

func TestResolveRelation_EmptySearchPath(t *testing.T) {
	withSearchPath(t, []string{}, func() {
		if r := testDb.ResolveRelation("", "director"); r != nil {
			t.Errorf("expected nil for an unqualified lookup against an empty search path, got %v", r)
		}
	})
	withSearchPath(t, nil, func() {
		if r := testDb.ResolveRelation("", "director"); r != nil {
			t.Errorf("expected nil for an unqualified lookup against a nil search path, got %v", r)
		}
	})
	// explicit schema must still work regardless of an empty search path —
	// it never consults SearchPath at all
	withSearchPath(t, []string{}, func() {
		if r := testDb.ResolveRelation("public", "director"); r == nil {
			t.Errorf("expected explicit-schema lookup to succeed even with an empty search path")
		}
	})
}

func TestResolveFunctionCandidates_EmptySearchPath(t *testing.T) {
	withSearchPath(t, []string{}, func() {
		if fns := testDb.ResolveFunctionCandidates("", "fn_overload"); fns != nil {
			t.Errorf("expected nil for an unqualified lookup against an empty search path, got %v", fns)
		}
	})
}

func TestResolveFunctionCandidates_OverloadSet(t *testing.T) {
	fns := testDb.ResolveFunctionCandidates("public", "fn_overload")
	if len(fns) != 2 {
		t.Fatalf("expected 2 overloads of fn_overload, got %d : %v", len(fns), fns)
	}
}

func TestResolveFunctionCandidates_NotFound(t *testing.T) {
	if fns := testDb.ResolveFunctionCandidates("public", "no_such_function"); fns != nil {
		t.Errorf("expected nil for an unknown function, got %v", fns)
	}
}

func TestFunction_AcceptsArity_Defaults(t *testing.T) {
	fns := testDb.ResolveFunctionCandidates("public", "fn_with_default")
	if len(fns) != 1 {
		t.Fatalf("expected exactly 1 fn_with_default, got %d", len(fns))
	}
	f := fns[0]
	cases := map[int]bool{0: false, 1: true, 2: true, 3: false}
	for n, want := range cases {
		if got := f.AcceptsArity(n); got != want {
			t.Errorf("AcceptsArity(%d) = %v, want %v (PgNargs=%d PgNargsDefaults=%d)", n, got, want, f.PgNargs, f.PgNargsDefaults)
		}
	}
}

func TestFunction_AcceptsArity_Variadic(t *testing.T) {
	fns := testDb.ResolveFunctionCandidates("public", "fn_variadic")
	if len(fns) != 1 {
		t.Fatalf("expected exactly 1 fn_variadic, got %d", len(fns))
	}
	f := fns[0]
	// a is required, rest is variadic (can absorb zero or many) : the
	// minimum callable arity is 1 (just "a"), and there is no maximum.
	cases := map[int]bool{0: false, 1: true, 2: true, 5: true}
	for n, want := range cases {
		if got := f.AcceptsArity(n); got != want {
			t.Errorf("AcceptsArity(%d) = %v, want %v (PgNargs=%d PgNargsDefaults=%d)", n, got, want, f.PgNargs, f.PgNargsDefaults)
		}
	}
}

func TestFunction_AcceptsArity_PureVariadic(t *testing.T) {
	fns := testDb.ResolveFunctionCandidates("public", "fn_pure_variadic")
	if len(fns) != 1 {
		t.Fatalf("expected exactly 1 fn_pure_variadic, got %d", len(fns))
	}
	f := fns[0]
	// No required (non-variadic) argument at all : the variadic slot alone
	// can absorb zero, so the minimum callable arity is 0, not PgNargs-1's
	// naive floor of 0 by coincidence here — this specifically exercises
	// the "no fixed arguments before the variadic one" case.
	cases := map[int]bool{0: true, 1: true, 5: true}
	for n, want := range cases {
		if got := f.AcceptsArity(n); got != want {
			t.Errorf("AcceptsArity(%d) = %v, want %v (PgNargs=%d PgNargsDefaults=%d)", n, got, want, f.PgNargs, f.PgNargsDefaults)
		}
	}
}

func TestFunction_AcceptsArity_AllDefaults(t *testing.T) {
	fns := testDb.ResolveFunctionCandidates("public", "fn_all_defaults")
	if len(fns) != 1 {
		t.Fatalf("expected exactly 1 fn_all_defaults, got %d", len(fns))
	}
	f := fns[0]
	cases := map[int]bool{0: true, 1: true, 2: true, 3: false}
	for n, want := range cases {
		if got := f.AcceptsArity(n); got != want {
			t.Errorf("AcceptsArity(%d) = %v, want %v (PgNargs=%d PgNargsDefaults=%d)", n, got, want, f.PgNargs, f.PgNargsDefaults)
		}
	}
}
