package dtoagent

// AgentCreateReq 注册自定义 agent（对既有 runtime 的命名配置，不写代码即新增）。
type AgentCreateReq struct {
	ID               string                `json:"id" binding:"required"`
	Name             string                `json:"name"`
	Runtimes         []string              `json:"runtimes" binding:"required"`
	Provider         string                `json:"provider,omitempty"`
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy,omitempty"`
}

// AgentUpdateReq PATCH agent 元数据（指针字段：nil 不更新）。
type AgentUpdateReq struct {
	Name             *string               `json:"name"`
	Runtimes         *[]string             `json:"runtimes"`
	Provider         *string               `json:"provider"`
	PermissionPolicy *PermissionPolicyResp `json:"permission_policy"`
}
