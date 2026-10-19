package provider

import "github.com/morehao/ark-mate/internal/object/objdelivery"

// ——— 供应商域的对外响应形状（v3.2 刀 8）———
//
// 只有字段、没有 json tag：对外形态（`dtoprovider.*`）由用例层 `service/svcprovider` 投影。
// 字段名与 DTO 一一对应（投影是纯搬运，不重命名）。
type ProviderItem struct {
	ID            string
	Name          string
	Protocol      string
	BaseURL       string
	HasKey        bool
	KeySource     string
	KeyRef        string
	Note          string
	Revision      int64
	UpdatedAt     string
	ModelCount    int
	Problems      []string
	Applicability []objdelivery.Applicability
	Refs          ProviderRefs
	Effect        *ProviderEffect
}

// ProviderRefs 供应商被谁引用（读面聚合）。
type ProviderRefs struct {
	Models   []string
	Runtimes []string
}

// ProviderEffect 改一个供应商对在跑会话的影响面。
type ProviderEffect struct {
	Channel          string
	Effect           string
	AffectedSessions int
	Deferred         int
	Note             string
}

// ProviderDelete 删除供应商的结果。
type ProviderDelete struct {
	Deleted            bool
	ReferencedByModels []string
	CredentialsLeft    string
}

// CreateRequest 新建供应商。
type CreateRequest struct {
	ID       string
	Name     *string
	Protocol string
	BaseURL  *string
	APIKey   *string
	Note     *string
}

// PatchRequest 改供应商（nil = 这个字段不动）。
type PatchRequest struct {
	Name     *string
	Protocol *string
	BaseURL  *string
	APIKey   *string
	Note     *string
}
