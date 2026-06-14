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
	health := "healthy"
	oomKilled := true
	created := time.Unix(1, 0).UTC()

	snap := Snapshot{
		AgentVersion: "1.2.3",
		CollectedAt:  time.Unix(0, 0).UTC(),
		Host:         Host{Name: "box", Platform: "linux"}, // UptimeSeconds nil
		CPU:          &Metric{Used: 0, Total: 100, Unit: "%", Percent: 0},
		Memory:       nil,              // not obtained
		Disk:         &Metric{Used: 0}, // obtained, genuinely zero
		Load:         &Load{One: 0, Five: 0, Fifteen: 0},
		Containers: []Container{{
			Name:      "web",
			Image:     "nginx:1.27",
			State:     "running",
			Health:    &health,
			CreatedAt: &created,
			OOMKilled: &oomKilled,
		}},
		Databases: nil,                          // not obtained
		Endpoints: []Endpoint{{Name: "x.test"}}, // names-only in v1
	}

	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)

	mustContain := []string{
		`"memory":null`,        // not obtained
		`"databases":null`,     // not obtained
		`"uptimeSeconds":null`, // not obtained
		`"state":"running"`,    // Docker state is factual
		`"health":"healthy"`,   // Docker health if exposed
		`"oomKilled":true`,     // Docker reason signal
		`"endpoints":[{"name":"x.test","source":null}]`, // endpoint name only
		`"load":{`,               // obtained
		`"cpu":{`,                // obtained
		`"disk":{`,               // obtained, genuinely zero
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
