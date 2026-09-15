package monitor

import (
	"math"
	"testing"
)

func TestCPUExcludesGuestDoubleCounting(t *testing.T) {
	total, idle, e := ParseCPU("cpu 100 20 30 400 50 6 7 8 90 10\n")
	if e != nil || total != 621 || idle != 450 {
		t.Fatalf("%d %d %v", total, idle, e)
	}
}
func TestMemoryAvailableIncludesReclaimable(t *testing.T) {
	total, available, e := ParseMemory("MemTotal: 1000 kB\nMemFree: 100 kB\nMemAvailable: 650 kB\n")
	if e != nil || total != 1024000 || available != 665600 {
		t.Fatal(total, available, e)
	}
	if _, _, e = ParseMemory("MemTotal: 1000 kB"); e == nil {
		t.Fatal("missing available must fail")
	}
}
func TestRateHandlesReset(t *testing.T) {
	if Rate(1, 100, 2) != 0 || Rate(100, 0, 0) != 0 || math.Abs(Rate(500, 100, 2)-200) > 0.01 {
		t.Fatal("invalid rate")
	}
}
