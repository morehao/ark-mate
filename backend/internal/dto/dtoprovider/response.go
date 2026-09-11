package dtoprovider

// ProviderResp provider 档案对外表示（密钥只以 has_key 呈现，绝不读回）。
type ProviderResp struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	Model  string `json:"model,omitempty"`
	HasKey bool   `json:"has_key"`
}
