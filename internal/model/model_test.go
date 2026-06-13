package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestContractNullVsZeroVsEmpty verifies the three distinct, honest states:
// value (obtained), 0/[] (obtained & genuinely zero/empty), null (not obtained).
func TestContractNullVsZeroVsEmpty(t *testing.T) {
	snap := Snapshot{
		AgentVersion: "1.2.3",
		CollectedAt:  time.Unix(0, 0).UTC(),
		Host:         Host{Name: "box", Platform: "linux"}, // UptimeSeconds nil
		CPU:          &Metric{Used: 0, Total: 100, Unit: "%", Percent: 0},
		Memory:       nil,                                                          // not obtained
		Disk:         &Metric{Used: 0},                                             // obtained, genuinely zero
		Network:      nil,                                                          // not obtained
		Containers:   []Container{},                                                // obtained, none found
		Databases:    nil,                                                          // not obtained
		Endpoints:    []Endpoint{{Name: "x", URL: "https://x/", Reachable: false}}, // ResponseMs/StatusCode nil
		Groups:       nil,
	}

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)

	mustContain := []string{
		`"memory":null`,          // not obtained
		`"network":null`,         // not obtained
		`"databases":null`,       // not obtained
		`"uptimeSeconds":null`,   // not obtained
		`"containers":[]`,        // obtained, empty
		`"cpu":{`,                // obtained
		`"disk":{`,               // obtained, genuinely zero
		`"responseMs":null`,      // unreachable endpoint -> not obtained
		`"statusCode":null`,      // unreachable endpoint -> not obtained
		`"agentVersion":"1.2.3"`, // always present
	}
	for _, want := range mustContain {
		if !strings.Contains(s, want) {
			t.Errorf("marshaled snapshot missing %s\nfull: %s", want, s)
		}
	}

	// A genuine zero must NOT be null.
	if strings.Contains(s, `"disk":null`) {
		t.Error("disk was obtained (zero) but marshaled as null")
	}
}
