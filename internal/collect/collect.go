// Package collect gathers a structured Snapshot of the host. Platform-specific
// readers live in sys_<os>.go files behind build tags; this file is portable
// and turns raw counter samples into rates and percentages.
//
// Availability: platform readers return an ok flag (false = "not obtained").
// When ok is false the corresponding Snapshot field is left nil so it marshals
// to JSON null per the data-availability contract (see model package).
package collect

import (
	"sync"
	"time"

	"github.com/AndiOliverIon/meerkat-agent/internal/identity"
	"github.com/AndiOliverIon/meerkat-agent/internal/model"
)

// Version is the agent version string surfaced in snapshots. It is a var (not a
// const) so release builds can stamp it from the git tag via the linker:
//
//	-ldflags "-X github.com/AndiOliverIon/meerkat-agent/internal/collect.Version=1.2.0"
//
// Source/dev builds keep the default below.
var Version = "0.0.0-dev"

// Collector holds the previous counter sample so it can derive CPU% and
// rates between reads. Safe for concurrent use.
type Collector struct {
	mu       sync.Mutex
	stateDir string

	hasPrev  bool
	prevTime time.Time
	prevCPUBusy,
	prevCPUTotal uint64
}

// New returns a ready Collector.
func New(stateDir ...string) *Collector {
	dir := identity.DefaultDir
	if len(stateDir) > 0 && stateDir[0] != "" {
		dir = stateDir[0]
	}
	return &Collector{stateDir: dir}
}

// Snapshot reads the host once and discovers its running resources. Rates
// CPU% is computed against the previous Snapshot call; the first call reports
// zero CPU rate.
func (c *Collector) Snapshot() model.Snapshot {
	snap := c.sampleCore()

	// Discovery touches the Docker socket and filesystem, so
	// it runs outside the collector lock to keep concurrent reads responsive.
	containers := readContainers()
	databases := readDatabases(c.stateDir, containers)
	endpoints := readEndpoints()

	snap.Containers = containers
	snap.Databases = databases
	snap.Endpoints = endpoints
	return snap
}

// sampleCore reads the fast kernel counters and derives the core metrics
// (host, CPU, memory, disk, load). It holds the lock only for the brief
// counter read; discovery fields are filled in by Snapshot.
func (c *Collector) sampleCore() model.Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	cpuBusy, cpuTotal, cpuOK := readCPUSample()

	var cpu *model.Metric
	if cpuOK {
		var cpuPct float64
		if c.hasPrev {
			if dTotal := cpuTotal - c.prevCPUTotal; dTotal > 0 {
				cpuPct = 100 * float64(cpuBusy-c.prevCPUBusy) / float64(dTotal)
			}
		}
		cpu = metric(cpuPct, 100, "%")
	}

	c.prevTime = now
	if cpuOK {
		c.prevCPUBusy, c.prevCPUTotal = cpuBusy, cpuTotal
	}
	c.hasPrev = true

	var memory *model.Metric
	if used, total, ok := readMem(); ok {
		memory = metric(used, total, "GB")
	}
	var disk *model.Metric
	if used, total, ok := readDisk("/"); ok {
		disk = metric(used, total, "GB")
	}
	var load *model.Load
	if one, five, fifteen, ok := readLoad(); ok {
		load = &model.Load{One: round2(one), Five: round2(five), Fifteen: round2(fifteen)}
	}

	return model.Snapshot{
		AgentVersion: Version,
		CollectedAt:  now,
		Host:         readHost(),
		CPU:          cpu,
		Memory:       memory,
		Disk:         disk,
		Load:         load,
		// Discovery fields are filled in by Snapshot after the lock is released.
	}
}

// Once returns a Snapshot with meaningful rates by sampling twice over a short
// interval. Intended for the one-shot CLI mode.
func (c *Collector) Once() model.Snapshot {
	c.sampleCore() // warm the counters so the second read has real rates
	time.Sleep(400 * time.Millisecond)
	return c.Snapshot()
}

// metric builds a non-nil Metric for an obtained reading.
func metric(used, total float64, unit string) *model.Metric {
	var pct float64
	if total > 0 {
		pct = 100 * used / total
	}
	if unit == "%" { // CPU: used already is the percentage
		pct = used
	}
	return &model.Metric{Used: round1(used), Total: round1(total), Unit: unit, Percent: round1(pct)}
}

func round1(v float64) float64 {
	return float64(int64(v*10+0.5)) / 10
}

func round2(v float64) float64 {
	return float64(int64(v*100+0.5)) / 100
}

// f64ptr / intptr are small helpers for building nullable contract fields.
func f64ptr(v float64) *float64 { return &v }
func intptr(v int) *int         { return &v }
func strptr(v string) *string   { return &v }
func boolptr(v bool) *bool      { return &v }
