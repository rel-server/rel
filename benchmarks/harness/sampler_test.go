package harness

import "testing"

func TestParseMiB(t *testing.T) {
	for in, want := range map[string]float64{"12.5MiB": 12.5, "1.5GiB": 1536, "2048kB": 2, "1024KiB": 1, "0B": 0} {
		if got := parseMiB(in); got != want {
			t.Errorf("parseMiB(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestPlanResources(t *testing.T) {
	if _, err := PlanResources(2); err == nil {
		t.Error("2 cores must be refused")
	}
	for _, n := range []int{3, 4, 8, 16, 64} {
		r, err := PlanResources(n)
		if err != nil {
			t.Fatal(err)
		}
		if len(r.LoadCPUs) < 1 || len(r.DBCPUs) < 1 || len(r.ServerCPUs) < 1 || len(r.LoadCPUs)+len(r.DBCPUs)+len(r.ServerCPUs) != n {
			t.Errorf("n=%d bad split %+v", n, r)
		}
	}
	if got := CPUSet([]int{4, 5, 6}); got != "4,5,6" {
		t.Errorf("CPUSet = %q", got)
	}
}
