package entity

import "time"

type BadHost struct {
	MachineID int
	Reason    string
	CreatedAt time.Time
}
