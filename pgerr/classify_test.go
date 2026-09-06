package pgerr

import (
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/rel-server/rel/errcode"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestClassify_NotAPgError(t *testing.T) {
	_, _, _, _, ok := Classify(fmt.Errorf("plain error"))
	if ok {
		t.Fatal("expected ok=false for a non-PgError")
	}
}

func TestClassify_RSxxxTakesPrecedence(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "RS404", Message: "not found"}
	wrapped := fmt.Errorf("item 0: %w", pgErr)

	status, code, _, detail, ok := Classify(wrapped)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if status != 404 {
		t.Errorf("status = %d, want 404", status)
	}
	if code != "RS404" {
		t.Errorf("code = %q, want RS404", code)
	}
	if detail.Message != "not found" {
		t.Errorf("detail.Message = %q, want %q", detail.Message, "not found")
	}
}

func TestClassify_ConstraintViolations(t *testing.T) {
	tests := []struct {
		sqlstate string
		wantCode errcode.Code
		wantTier Tier
	}{
		{"23505", "PG_UNIQUE_VIOLATION", TierConstraintViolation},
		{"23503", "PG_FOREIGN_KEY_VIOLATION", TierConstraintViolation},
		{"23502", "PG_NOT_NULL_VIOLATION", TierConstraintViolation},
		{"23514", "PG_CHECK_VIOLATION", TierConstraintViolation},
	}
	for _, tt := range tests {
		t.Run(tt.sqlstate, func(t *testing.T) {
			pgErr := &pgconn.PgError{
				Code:           tt.sqlstate,
				Message:        "boom",
				ConstraintName: "some_constraint",
				TableName:      "some_table",
			}
			_, code, tier, detail, ok := Classify(pgErr)
			if !ok {
				t.Fatal("expected ok=true")
			}
			if code != tt.wantCode {
				t.Errorf("code = %q, want %q", code, tt.wantCode)
			}
			if tier != tt.wantTier {
				t.Errorf("tier = %v, want %v", tier, tt.wantTier)
			}
			if detail.ConstraintName != "some_constraint" || detail.TableName != "some_table" {
				t.Errorf("detail fields not populated: %#v", detail)
			}
		})
	}
}

func TestClassify_PermissionDenied(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "42501", Message: "permission denied for table foo", TableName: "foo"}
	status, code, tier, detail, ok := Classify(pgErr)
	if !ok || status != 403 || code != "PG_PERMISSION_DENIED" || tier != TierPermissionDenied {
		t.Fatalf("got status=%d code=%q tier=%v ok=%v", status, code, tier, ok)
	}
	if detail.Message != "permission denied for table foo" {
		t.Errorf("detail.Message = %q", detail.Message)
	}
}

func TestClassify_UnclassifiedFallsBackTo500(t *testing.T) {
	pgErr := &pgconn.PgError{Code: "XX000", Message: "internal error"}
	status, code, tier, detail, ok := Classify(pgErr)
	if !ok || status != 500 || code != errcode.Internal || tier != TierUnclassified {
		t.Fatalf("got status=%d code=%q tier=%v ok=%v", status, code, tier, ok)
	}
	if detail.Message != "internal error" {
		t.Errorf("detail.Message = %q", detail.Message)
	}
}

// TestDetail_FieldAllowList is a reflection tripwire : Detail must never
// grow a Where/InternalQuery/Position/File/Line/Routine field silently.
func TestDetail_FieldAllowList(t *testing.T) {
	want := []string{"Message", "Detail", "SchemaName", "TableName", "ColumnName", "ConstraintName"}
	sort.Strings(want)

	var got []string
	for f := range reflect.TypeFor[Detail]().Fields() {
		got = append(got, f.Name)
	}
	sort.Strings(got)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Detail's fields = %v, want exactly %v — see specs/error-handling.md ### `pg_error` field allow-list", got, want)
	}
}
