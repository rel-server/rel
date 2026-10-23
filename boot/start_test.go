package boot

import (
	"strings"
	"testing"
)

func TestRedactedTarget_NeverIncludesCredentials(t *testing.T) {
	got := redactedTarget("postgres://secret_user:secret_pass@db.internal:5432/mydb")
	if got != "db.internal:5432/mydb" {
		t.Errorf("redactedTarget = %q, want %q", got, "db.internal:5432/mydb")
	}
	if strings.Contains(got, "secret_user") || strings.Contains(got, "secret_pass") {
		t.Errorf("redactedTarget leaked a credential: %q", got)
	}
}
