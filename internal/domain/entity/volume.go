package entity

import "time"

type Volume struct {
	ID         int64
	VastaiID   int64
	VolumeName string
	SizeGB     int
	MountPath  string
	MachineID  int
	CreatedAt  time.Time
}
