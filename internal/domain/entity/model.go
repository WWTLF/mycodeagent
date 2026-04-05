package entity

type ModelCategory string

const (
	CategoryCoding  ModelCategory = "coding"
	CategoryFiction ModelCategory = "fiction"
	CategoryDolphin ModelCategory = "dolphin"
)

type Model struct {
	Name        string
	Alias       string
	HFRepo      string
	Category    ModelCategory
	VRAM        int     // GB required per GPU
	NumGPUs     int     // number of GPUs needed (0 or 1 = single GPU)
	Temperature float64 // default serving temperature
	VLLMArgs    []string
}
