package service

// RemoteInstance is a domain DTO that represents a vast.ai instance as the
// VastaiProvider sees it. It is the read-side counterpart to the entity.Instance
// stored locally — the adapter maps the raw vast.ai response into this shape so
// the domain layer never imports the infrastructure/vastai package.
type RemoteInstance struct {
	VastaiID     int
	ActualStatus string
	CurState     string
	StatusMsg    string
	SSHHost      string
	PublicIPAddr string
	SSHPort      int
	HourlyRate   float64
	Onstart      string
}
