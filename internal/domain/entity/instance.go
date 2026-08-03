package entity

import (
	"strings"
	"time"
)

// DefaultSyncRootName is the directory created in the working directory when no
// --sync-folder is given.
//
// It lives in the domain because three layers need to name it — the command help
// text, the deploy options doc, and the rsync loop that creates it — and the
// commands and application layers are forbidden from importing infrastructure,
// where the loop lives.
//
// The name predates Jupyter and is kept for instances already syncing into it,
// though it now covers notebooks as well as ComfyUI output.
const DefaultSyncRootName = "COMFY_SYNC"

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
	// SyncRoot. Tracked like TunnelPID so `tunnel` can restart it and
	// `stop`/`kill` can end it instead of leaking a process per deploy.
	SyncPID int
	// SyncRoot is the absolute local directory the loop syncs into, recorded at
	// deploy time.
	//
	// It is persisted rather than recomputed because the root used to be
	// "<cwd>/COMFY_SYNC", and cwd is a property of whichever shell happened to
	// run the command. `init` in one directory and `tunnel` in another already
	// produced two different roots, silently splitting one instance's files
	// across two places; --sync-folder would have widened that from a directory
	// mistake into an arbitrary one. Storing the resolved path means every later
	// command targets what the deploy actually chose.
	//
	// Empty on rows written before this existed: callers fall back to the old
	// cwd-derived default, which is what those loops are already using.
	SyncRoot  string
	CreatedAt time.Time
}
