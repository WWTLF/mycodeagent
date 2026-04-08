package entity

import "time"

type InstanceStatus string

const (
	StatusStarting InstanceStatus = "starting"
	StatusRunning  InstanceStatus = "running"
	StatusStopped  InstanceStatus = "stopped"
	StatusError    InstanceStatus = "error"
)

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
	VolumeID   int64
	NumGPUs    int // actual GPU count from the offer; needed for restart so vLLM keeps the same --tensor-parallel-size
	VolumeName string // mirrors the vast.ai "V.<id>" name; populated by Sync from ExtraEnv "-v V.123:/mount" entries so ps can render it without re-querying vast.ai
	CreatedAt  time.Time
}
