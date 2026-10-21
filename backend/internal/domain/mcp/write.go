package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/morehao/ark-mate/internal/core/failure"
	"strings"

	"github.com/morehao/ark-mate/internal/core/capability"
	defn "github.com/morehao/ark-mate/internal/core/definition"

	"github.com/morehao/golib/glog"
)

// ——— MCP server 定义的写面（新建 / 覆盖写 / 删除） ———
//
// 与技能写面同形（先校验 → 再提交 → 最后入表），差别全在**闸门**上：MCP 定义能指定拉起什么
// 进程、塞什么环境变量，所以 `capability.ValidateMcpServer` 的门比 skill 多得多——transport
// 枚举、stdio 命令必须绝对路径、`store:`/`env:` 引用词法、明文与密钥键不得重复、明文值不得为空。
// 写面**一个门都不新开也不放行**：定义怎么进注册表，走的是同一条路。
//
// 顺序不可换：① 先校验（看到的是**合并后**的完整定义）② 通过才提交给载体（C9 文件纪律 / DB
// revision CAS）③ 提交成功后才更新内存。顺序倒过来就会留下"内存里有、载体上没有"的幻影。

// WriteServer 写面主体（create / update 共用的全部顺序与失败处理）。
func (p *Plane) WriteServer(ctx context.Context, pathID string, req WriteRequest, create bool) (*WriteResult, error) {
	if p.caps == nil {
		return nil, defn.ErrCapabilityUnavailable()
	}
	repo, err := p.defs.RepoFor(defn.CapSubMcps)
	if err != nil {
		return nil, err
	}
	patch := defn.RawPatch(req)

	id := pathID
	bodyID, hasBodyID, err := defn.RawString(req, "id")
	if err != nil {
		return nil, err
	}
	if create {
		if !hasBodyID || strings.TrimSpace(bodyID) == "" {
			return nil, defn.InvalidParamErr("新建 MCP server 必须带 id")
		}
		id = bodyID
	} else {
		if hasBodyID && bodyID != pathID {
			return nil, defn.InvalidParamErr(fmt.Sprintf(
				"路径 id %q 与 body id %q 不一致（id 不可改：要换 id 就删掉重建）", pathID, bodyID))
		}
		patch["id"] = pathID
	}

	if !defn.ValidDefinitionID(id) {
		if create {
			return nil, defn.InvalidDefinitionIDErr("mcp", id)
		}
		if _, in := p.caps.GetMcpServer(id); !in {
			return nil, notFoundErr(id)
		}
		return nil, defn.InvalidDefinitionIDErr("mcp", id)
	}

	_, inReg := p.caps.GetMcpServer(id)
	stored, err := repo.Exists(ctx, id)
	if err != nil {
		return nil, err
	}

	if create {
		if inReg || stored {
			return nil, existsErr(id)
		}
	} else if !inReg && !stored {
		return nil, notFoundErr(id)
	}

	doc, rev, existed, err := repo.Open(ctx, id, patch)
	if err != nil {
		return nil, err
	}
	merged, err := doc.Marshal()
	if err != nil {
		return nil, err
	}
	var m capability.McpServer
	if err := json.Unmarshal(merged, &m); err != nil {
		return nil, defn.InvalidParamErr(fmt.Sprintf("MCP server 定义无法解析：%v", err))
	}
	if m.ID == "" {
		m.ID = id
	}
	m.SourceFile = repo.SourceFile(id)

	if err := capability.ValidateMcpServer(&m); err != nil {
		// 闸门拒绝 → 40001：transport 枚举 / stdio 绝对路径 / 引用词法 / 明文密钥键重复…
		// 文案直接带闸门原话（它已经说清哪一项不合法），不另造一层解释。
		return nil, failure.NewError(failure.ClassRequest, "definition_invalid",
			fmt.Sprintf("MCP server %q 的定义被拒绝：%v", id, err), err)
	}
	created, conflict, err := repo.Commit(ctx, id, doc, rev, existed)
	if err != nil {
		return nil, err
	}
	if conflict {
		// 载体的乐观锁没中（文件被手改 / 行 revision 变了 / 别人抢先建出来）→ 40901。
		return nil, defn.DefinitionChangedErr(repo.Locator(id))
	}
	// 提交成功后才入表：同一份定义刚过闸门（纯函数），这一次必然成功（详见 skill 写面）。
	if err := p.caps.RegisterMcpServer(&m); err != nil {
		glog.Warnw(ctx, "capability: mcp 定义已提交但入表失败（内存视图可能落后）", "id", id, "err", err)
	}

	resp := &WriteResult{
		ID: id, Path: repo.Locator(id), Created: create || created,
		UnresolvedSecretKeys: p.secrets.UnresolvedKeys(m),
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

// DeleteServer 删除一个 MCP server 定义（DELETE /mcps/:id）：删定义 + 摘内存条目
// + **一并摘除该 server 归属的工具**。幂等：不存在 → `deleted:false` 且无错。
func (p *Plane) DeleteServer(ctx context.Context, id string) (*DeleteResult, error) {
	if p.caps == nil {
		return nil, defn.ErrCapabilityUnavailable()
	}
	repo, err := p.defs.RepoFor(defn.CapSubMcps)
	if err != nil {
		return nil, err
	}
	if !defn.ValidDefinitionID(id) {
		if _, in := p.caps.GetMcpServer(id); !in {
			return deleteNoop(id, ""), nil
		}
		return nil, defn.InvalidDefinitionIDErr("mcp", id)
	}
	_, inReg := p.caps.GetMcpServer(id)
	stored, err := repo.Exists(ctx, id)
	if err != nil {
		return nil, err
	}
	locator := repo.Locator(id)
	if !inReg && !stored {
		return deleteNoop(id, locator), nil
	}

	deleted, conflict, err := repo.Remove(ctx, id)
	if err != nil {
		return nil, err
	}
	if conflict {
		return nil, defn.DefinitionChangedErr(locator)
	}
	p.caps.RemoveMcpServer(id)
	// 工具条目必须一起摘：留着已删 server 的工具，用户会在 /tools 里看见一个永远调不通的能力
	// （agent 侧可见性过滤只看交通承载，看不出 server 已经没了）。
	tools := p.caps.RemoveToolsForMcpServer(id)
	resp := &DeleteResult{ID: id, Deleted: deleted, Path: locator, ToolsRemoved: tools}
	resp.NextAction = fmt.Sprintf(
		"已删除：定义已移除，归属该 server 的 %d 条工具条目已一并摘除（正在跑的会话不受影响，下一轮/下次拉起不再注入）", tools)
	return resp, nil
}

// deleteNoop 幂等删除的空结果。
func deleteNoop(id, path string) *DeleteResult {
	return &DeleteResult{ID: id, Deleted: false, Path: path,
		NextAction: "该 MCP server 本来就不存在，无需删除"}
}

// existsErr 新建时 id 已占用（40901）。
func existsErr(id string) error {
	return failure.NewError(failure.ClassLimit, "mcp_exists",
		fmt.Sprintf("MCP server %q 已存在：改它用 PUT /mcps/%s，或换一个 id", id, id), nil)
}

// notFoundErr 目标 MCP server 不存在（40401）。
//
// 它是**这个平面唯一的 40401 出口**：写面、删除面、实时面、探测面都经它——四处各写一遍的
// 后果不是编译错误，而是某一天只改了三处、第四个出口的 code 悄悄变成别的值。
func notFoundErr(id string) error {
	return failure.NewError(failure.ClassRequest, "mcp_not_found", fmt.Sprintf("未知 MCP server %q", id), nil)
}
