package entity

import (
	"strings"
	"time"
)

type InstanceStatus string

const (
	StatusStarting InstanceStatus = "starting"
	StatusRunning  InstanceStatus = "running"
	StatusStopped  InstanceStatus = "stopped"
	StatusError    InstanceStatus = "error"
)

// Is reports whether the status is base, allowing for the " (detail)" suffix
// that Sync appends when vast.ai returns a status_msg — a running instance can
// be recorded as "running (loading container)". Every status comparison must go
// through this: an exact `== StatusRunning` silently skips such rows, which is
// how the budget totals used to lose instances.
func (s InstanceStatus) Is(base InstanceStatus) bool {
	return s == base || strings.HasPrefix(string(s), string(base)+" ")
}

type Instance struct {
	ID         int64
	VastaiID   int64
	ModelName  string
	Status     InstanceStatus
	LocalPort  int
	SSHHost    string
	SSHPort    int
	TunnelPID  int
	HourlyRate float64
	NumGPUs    int // actual GPU count from the offer; needed for restart so the server is relaunched with the same GPU split
	// ContextLength is the window the server was actually launched with — the
	// scaled value, not the catalog baseline. Persisted for the same reason as
	// NumGPUs: at restart time the offer is gone, and re-emitting the baseline
	// would silently shrink the window on a rental with fatter GPUs.
	ContextLength int
	// SyncPID is the detached rsync loop pulling the engine's output into
	// ./COMFY_SYNC. Tracked like TunnelPID so `tunnel` can restart it and
	// `stop`/`kill` can end it instead of leaking a process per deploy.
	SyncPID   int
	CreatedAt time.Time
}
