package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/morehao/ark-mate/internal/dto/dtocapability"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/capability"
)

// ——— M2-3：MCP server 定义的写面 ———
//
// 与 skill 写面同形（先校验 → 再提交 → 最后入表，见 skills_write.go），差别全在**闸门**上：
// MCP 定义能指定拉起什么进程、塞什么环境变量，所以 `ValidateMcpServer` 的门比 skill 多得多
// ——transport 枚举、stdio 命令必须绝对路径、`vault:`/`env:` 引用词法、明文与密钥键不得重复、
// 明文值不得为空。写面**一个门都不新开也不放行**：定义怎么进注册表，走的是同一条路。

// CreateMcpServer 新建一个 MCP server 定义（POST /mcps）：id 必填，已存在 → `mcp_exists`(40901)。
func (s *Service) CreateMcpServer(ctx context.Context, req dtocapability.McpWriteReq) (*dtocapability.McpWriteResp, error) {
	return s.writeMcp(ctx, "", req, true)
}

// UpdateMcpServer 覆盖写一个 MCP server 定义（PUT /mcps/:id）。键的出现与否有语义，同 skill 写面。
func (s *Service) UpdateMcpServer(ctx context.Context, id string, req dtocapability.McpWriteReq) (*dtocapability.McpWriteResp, error) {
	return s.writeMcp(ctx, id, req, false)
}

func (s *Service) writeMcp(ctx context.Context, pathID string, req dtocapability.McpWriteReq, create bool) (*dtocapability.McpWriteResp, error) {
	if s.caps == nil {
		return nil, errCapabilityUnavailable()
	}
	repo, err := s.definitionRepoFor(capSubMcps)
	if err != nil {
		return nil, err
	}
	patch := rawPatch(req)

	id := pathID
	bodyID, hasBodyID, err := rawString(req, "id")
	if err != nil {
		return nil, err
	}
	if create {
		if !hasBodyID || strings.TrimSpace(bodyID) == "" {
			return nil, capWriteErrParam("新建 MCP server 必须带 id")
		}
		id = bodyID
	} else {
		if hasBodyID && bodyID != pathID {
			return nil, capWriteErrParam(fmt.Sprintf(
				"路径 id %q 与 body id %q 不一致（id 不可改：要换 id 就删掉重建）", pathID, bodyID))
		}
		patch["id"] = pathID
	}

	if !validDefinitionID(id) {
		if create {
			return nil, invalidDefinitionIDErr("mcp", id)
		}
		if _, in := s.caps.GetMcpServer(id); !in {
			return nil, mcpNotFoundErr(id)
		}
		return nil, invalidDefinitionIDErr("mcp", id)
	}

	_, inReg := s.caps.GetMcpServer(id)
	stored, err := repo.exists(ctx, id)
	if err != nil {
		return nil, err
	}

	if create {
		if inReg || stored {
			return nil, mcpExistsErr(id)
		}
	} else if !inReg && !stored {
		return nil, mcpNotFoundErr(id)
	}

	doc, rev, existed, err := repo.open(ctx, id, patch)
	if err != nil {
		return nil, err
	}
	merged, err := doc.Marshal()
	if err != nil {
		return nil, err
	}
	var m capability.McpServer
	if err := json.Unmarshal(merged, &m); err != nil {
		return nil, capWriteErrParam(fmt.Sprintf("MCP server 定义无法解析：%v", err))
	}
	if m.ID == "" {
		m.ID = id
	}
	m.SourceFile = repo.sourceFile(id)

	if err := capability.ValidateMcpServer(&m); err != nil {
		// 闸门拒绝 → 40001：transport 枚举 / stdio 绝对路径 / 引用词法 / 明文密钥键重复…
		// 文案直接带闸门原话（它已经说清哪一项不合法），不另造一层解释。
		return nil, acl.NewError(acl.ClassRequest, "definition_invalid",
			fmt.Sprintf("MCP server %q 的定义被拒绝：%v", id, err), err)
	}
	created, conflict, err := repo.commit(ctx, id, doc, rev, existed)
	if err != nil {
		return nil, err
	}
	if conflict {
		// 载体的乐观锁没中（文件被手改 / 行 revision 变了 / 别人抢先建出来）→ 40901。
		return nil, definitionChangedErr(repo.locator(id))
	}
	// 提交成功后才入表：同一份定义刚过闸门（纯函数），这一次必然成功（详见 skills_write.go）。
	if err := s.caps.RegisterMcpServer(&m); err != nil {
		slog.Warn("capability: mcp 定义已提交但入表失败（内存视图可能落后）", "id", id, "err", err)
	}

	resp := &dtocapability.McpWriteResp{
		ID: id, Path: repo.locator(id), Created: create || created,
		UnresolvedSecretKeys: s.unresolvedSecretKeys(m),
	}
	switch {
	case create:
		resp.NextAction = "已新建：该 server 会在下一次会话/拉起时注入（禁用的 server 不注入）；探测用 POST /mcps/" + id + "/probe"
	case created:
		resp.NextAction = "已把该 server 落成定义（此前只存在于内存/代码注入）"
	default:
		resp.NextAction = "已覆盖写：只改了请求体里出现的键（其余键与未知键原样保留）；正在跑的会话不受影响，下一轮/下次拉起生效"
	}
	if len(resp.UnresolvedSecretKeys) > 0 {
		resp.NextAction += "；注意有密钥引用当前解析不出值，这些键在注入时会被跳过（详见 unresolved_secret_keys）"
	}
	return resp, nil
}

// DeleteMcpServer 删除一个 MCP server 定义（DELETE /mcps/:id）：删定义 + 摘内存条目
// + **一并摘除该 server 归属的工具**。幂等：不存在 → `deleted:false` 且无错。
func (s *Service) DeleteMcpServer(ctx context.Context, id string) (*dtocapability.McpDeleteResp, error) {
	if s.caps == nil {
		return nil, errCapabilityUnavailable()
	}
	repo, err := s.definitionRepoFor(capSubMcps)
	if err != nil {
		return nil, err
	}
	if !validDefinitionID(id) {
		if _, in := s.caps.GetMcpServer(id); !in {
			return mcpDeleteNoop(id, ""), nil
		}
		return nil, invalidDefinitionIDErr("mcp", id)
	}
	_, inReg := s.caps.GetMcpServer(id)
	stored, err := repo.exists(ctx, id)
	if err != nil {
		return nil, err
	}
	locator := repo.locator(id)
	if !inReg && !stored {
		return mcpDeleteNoop(id, locator), nil
	}

	deleted, conflict, err := repo.remove(ctx, id)
	if err != nil {
		return nil, err
	}
	if conflict {
		return nil, definitionChangedErr(locator)
	}
	s.caps.RemoveMcpServer(id)
	// 工具条目必须一起摘：留着已删 server 的工具，用户会在 /tools 里看见一个永远调不通的能力
	// （agent 侧可见性过滤只看交通承载，看不出 server 已经没了）。
	tools := s.caps.RemoveToolsForMcpServer(id)
	resp := &dtocapability.McpDeleteResp{ID: id, Deleted: deleted, Path: locator, ToolsRemoved: tools}
	resp.NextAction = fmt.Sprintf(
		"已删除：定义已移除，归属该 server 的 %d 条工具条目已一并摘除（正在跑的会话不受影响，下一轮/下次拉起不再注入）", tools)
	return resp, nil
}

// mcpDeleteNoop 幂等删除的空结果。
func mcpDeleteNoop(id, path string) *dtocapability.McpDeleteResp {
	return &dtocapability.McpDeleteResp{ID: id, Deleted: false, Path: path,
		NextAction: "该 MCP server 本来就不存在，无需删除"}
}

// mcpExistsErr 新建时 id 已占用（40901）。
func mcpExistsErr(id string) error {
	return acl.NewError(acl.ClassLimit, "mcp_exists",
		fmt.Sprintf("MCP server %q 已存在：改它用 PUT /mcps/%s，或换一个 id", id, id), nil)
}

// mcpNotFoundErr 目标 MCP server 不存在（40401）。
func mcpNotFoundErr(id string) error {
	return acl.NewError(acl.ClassRequest, "mcp_not_found", fmt.Sprintf("未知 MCP server %q", id), nil)
}
