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
	CreatedAt  time.Time
}
