package provider

// ——— 模型目录的对外形状（v3.2 刀 8）———
//
// 只有字段、没有 json tag：对外形态（`dtomodel.*`）由用例层投影。
// 字段名与 DTO 一一对应（投影是纯搬运）。
type CatalogView struct {
	Runtime    string
	Source     string
	ImageInput *bool
	FetchedAt  int64

	Options      []CatalogOption
	AuthGuidance []AuthGuide
	Note         string
}

type CatalogOption struct {
	ID           string
	Name         string
	Description  string
	Category     string
	Type         string
	CurrentValue string
	Grouped      bool
	Values       []CatalogOptionValue
}

type CatalogOptionValue struct {
	Value             string
	Name              string
	Description       string
	Group             string
	GroupName         string
	Unavailable       bool
	UnavailableReason string
	ModelID           string
}

type AdvertisedModel struct {
	Value             string
	Name              string
	Group             string
	GroupName         string
	Unavailable       bool
	UnavailableReason string
}

type AuthGuide struct {
	MethodID    string
	Name        string
	Description string
	Terminal    bool
	Command     []string
}

type ModelItem struct {
	ID              string
	ProviderID      string
	Model           string
	Name            string
	Note            string
	InputModalities []string
	MaxInputTokens  *int64
	MaxOutputTokens *int64
	Revision        int64
	UpdatedAt       int64
}
