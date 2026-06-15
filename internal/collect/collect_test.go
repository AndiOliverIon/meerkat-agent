package collect

import "testing"

func TestDiskUsageFromStatfsUsesDFSemantics(t *testing.T) {
	usedGB, totalGB, percent := diskUsageFromStatfs(1000, 800, 700, 1_000_000_000)

	if usedGB != 200 {
		t.Fatalf("usedGB = %v, want 200", usedGB)
	}
	if totalGB != 1000 {
		t.Fatalf("totalGB = %v, want 1000", totalGB)
	}
	if round1(percent) != 22.2 {
		t.Fatalf("percent = %v, want 22.2", round1(percent))
	}
}
