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
//	null/nil   -> NOT obtained (no privilege, error, unsupported, ...) -> "N/A"
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
	CPU          *Metric     `json:"cpu"`     // null if not obtained
	Memory       *Metric     `json:"memory"`  // null if not obtained
	Disk         *Metric     `json:"disk"`    // null if not obtained
	Network      *Network    `json:"network"` // null if not obtained
	Groups       []Group     `json:"groups"`
	Containers   []Container `json:"containers"`
	Databases    []Database  `json:"databases"`
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

// Network reports the throughput rate derived from sampling byte counters.
type Network struct {
	Interface string  `json:"interface"`
	RxMbps    float64 `json:"rxMbps"`
	TxMbps    float64 `json:"txMbps"`
}

// Group is an auto-discovered application grouped by name prefix.
type Group struct {
	Name       string      `json:"name"`
	Components []Component `json:"components"`
}

// Component is one part of a discovered application.
type Component struct {
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`      // app | database | container | endpoint
	StorageGB *float64 `json:"storageGB"` // null if not obtained
	Status    string   `json:"status"`
	Detail    string   `json:"detail"`
}

// Container is a running (or stopped) container on the host.
type Container struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Running bool   `json:"running"`
	CPU     *int   `json:"cpuPercent"` // null if not obtained (e.g. stopped)
	MemMB   *int   `json:"memMB"`      // null if not obtained
}

// Database is a discovered database instance/file.
type Database struct {
	Name   string   `json:"name"`
	Engine string   `json:"engine"`
	SizeGB *float64 `json:"sizeGB"` // null if not obtained (e.g. no read access)
	Status string   `json:"status"`
}

// Endpoint is a reachability check result.
type Endpoint struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	Reachable  bool   `json:"reachable"`
	ResponseMs *int   `json:"responseMs"` // null if not obtained (unreachable)
	StatusCode *int   `json:"statusCode"` // null if not obtained (unreachable)
}
