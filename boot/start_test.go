package boot

import "testing"

func TestRedactedTarget_NeverIncludesCredentials(t *testing.T) {
	got := redactedTarget("postgres://secret_user:secret_pass@db.internal:5432/mydb")
	if got != "db.internal:5432/mydb" {
		t.Errorf("redactedTarget = %q, want %q", got, "db.internal:5432/mydb")
	}
}
