package entity

type BudgetEntry struct {
	InstanceID int64
	ModelName  string
	HourlyRate float64
	Uptime     float64 // hours
	TotalCost  float64
}
