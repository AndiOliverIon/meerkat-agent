// Package model defines the JSON contract the agent exposes. Its shape
// deliberately mirrors the iOS app's model so the app and agent share one
// contract.
//
// Data-availability contract: a property the agent could not obtain a
// trustworthy value for is emitted as JSON null (never a faked 0).
// In Go that is a nil pointer / nil slice. So readers must distinguish three
// states:
//
//	value      -> obtained
//	0 or []    -> obtained, and genuinely zero / collected-but-empty
//	null/nil   -> NOT obtained (no privilege, error, unsupported, ...)
//
// Always-present fields (agentVersion, collectedAt, host name/os/...) are never
// null; fields that can be unavailable use pointer or slice types.
package model

import "time"

// Snapshot is the full structured view of a server at a point in time.
type Snapshot struct {
	AgentVersion string      `json:"agentVersion"`
	CollectedAt  time.Time   `json:"collectedAt"`
	Host         Host        `json:"host"`
	CPU          *Metric     `json:"cpu"`    // null if not obtained
	Memory       *Metric     `json:"memory"` // null if not obtained
	Disk         *Metric     `json:"disk"`   // null if not obtained
	Load         *Load       `json:"load"`   // null if not obtained
	Containers   []Container `json:"containers"`
	Databases    []Database  `json:"databases"`
	SQLServers   []SQLServer `json:"sqlServers"`
	Endpoints    []Endpoint  `json:"endpoints"`
}

// Host describes the machine the agent runs on.
type Host struct {
	Name          string  `json:"name"`
	OS            string  `json:"os"`
	Kernel        string  `json:"kernel"`
	Arch          string  `json:"arch"`
	Platform      string  `json:"platform"`      // "linux" | "darwin"
	UptimeSeconds *uint64 `json:"uptimeSeconds"` // null if not obtained
}

// Metric is a used/total pair plus a precomputed percentage.
// For CPU the unit is "%" and Total is 100. A whole Metric is null in the
// Snapshot when the metric could not be obtained on this platform.
type Metric struct {
	Used    float64 `json:"used"`
	Total   float64 `json:"total"`
	Unit    string  `json:"unit"`
	Percent float64 `json:"percent"`
}

// Load reports system load averages when available.
type Load struct {
	One     float64 `json:"one"`
	Five    float64 `json:"five"`
	Fifteen float64 `json:"fifteen"`
}

// Container is a running (or stopped) container on the host.
type Container struct {
	Name         string     `json:"name"`
	Image        string     `json:"image"`
	State        string     `json:"state"`        // running | exited | restarting | paused | dead | ...
	Health       *string    `json:"health"`       // null if no Docker health check exists
	RestartCount *int       `json:"restartCount"` // null if not obtained
	Ports        []string   `json:"ports"`        // [] if obtained and no ports
	CreatedAt    *time.Time `json:"createdAt"`    // null if not obtained
	StartedAt    *time.Time `json:"startedAt"`    // null if not obtained
	FinishedAt   *time.Time `json:"finishedAt"`   // null if not obtained
	ExitCode     *int       `json:"exitCode"`     // null if not obtained
	OOMKilled    *bool      `json:"oomKilled"`    // null if not obtained
	Error        *string    `json:"error"`        // null if none/not obtained
}

// Database is a discovered database instance/file.
type Database struct {
	Name          string     `json:"name"`
	Engine        string     `json:"engine"`
	SizeGB        *float64   `json:"sizeGB"` // null if not obtained (e.g. no read access)
	Status        string     `json:"status"` // detected | unavailable | running | stopped
	State         *string    `json:"state,omitempty"`
	RecoveryModel *string    `json:"recoveryModel,omitempty"`
	CreatedAt     *time.Time `json:"createdAt,omitempty"`
}

// SQLServer reports SQL Server instance-level pressure signals.
type SQLServer struct {
	Name                      string   `json:"name"`
	Container                 string   `json:"container,omitempty"`
	Status                    string   `json:"status"` // ok | warn | critical
	MemoryUsedMB              *float64 `json:"memoryUsedMB,omitempty"`
	MemoryTargetMB            *float64 `json:"memoryTargetMB,omitempty"`
	MemoryLimitMB             *float64 `json:"memoryLimitMB,omitempty"`
	MemoryUsedPercentOfTarget *float64 `json:"memoryUsedPercentOfTarget,omitempty"`
	PageLifeExpectancySeconds *float64 `json:"pageLifeExpectancySeconds,omitempty"`
	ProcessPhysicalMemoryLow  *bool    `json:"processPhysicalMemoryLow,omitempty"`
	SystemPhysicalMemoryLow   *bool    `json:"systemPhysicalMemoryLow,omitempty"`
	Signals                   []string `json:"signals"`
}

// Endpoint is a hostname/domain detected from supported web-server config.
type Endpoint struct {
	Name   string  `json:"name"`
	Source *string `json:"source"` // nginx | apache | caddy when known
}
