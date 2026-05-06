package cobaltapi

type ScaleService struct {
	Name     string `json:"name"`
	Replicas int    `json:"replicas"`
}

type ScaleInfo struct {
	Services []ScaleService `json:"services"`
	Project  string         `json:"project"`
}

func NewScaleInfo(project string) ScaleInfo {
	return ScaleInfo{
		Project:  project,
		Services: []ScaleService{},
	}
}

type ScaleSetRequest struct {
	Services []ScaleService `json:"services"`
}
