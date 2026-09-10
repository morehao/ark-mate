package dtoprovider

// ProviderPatchReq PATCH provider 档案：type/model 指针更新；key 非 nil 即覆盖/清除（空串清除）。
type ProviderPatchReq struct {
	Type  *string `json:"type"`
	Model *string `json:"model"`
	Key   *string `json:"key"`
}
