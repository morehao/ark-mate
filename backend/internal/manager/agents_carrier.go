package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/golib/glog"
)

// ——— agent 定义读写面的**载体**（M6-6） ———
//
// 与能力面（capability_repo.go）同源的分派判据（`s.st != nil`），但形态不同、刻意不复用同一层
// 抽象：agent 的写面是 **PATCH 语义**（没有 PUT），载荷是 typed 结构（`agentDefinitionFile`）
// 而不是"原始 JSON + 保留未知键"的文档，所以"读草稿 → patch → 提交"那套在这里没有对应的东西。
// 共享的是**语义承诺**而不是代码：
//
//	memory 模式：<AgentDir>/<id>.json（typed marshal + 同目录 tmp + rename）
//	sqlite 模式：agents 表（事务 + 行 revision CAS + 同事务一条审计）
//
// 两者的对外承诺相同：未知 agent → `agent_not_found`；种子 → `seed_agent_readonly`；
// 提交撞上并发写 → `definition_changed`(40901)；**失败即无副作用**（内存与载体不会各改一半）。

// agentDirSnapshot Agent definitions 目录的快照（空 = 未启用 definitions 目录）。
func (s *Service) agentDirSnapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.agentDir
}

// agentLocator agent 定义的定位串（人类可读；与能力面的 `path` 字段同一用途）。
func (s *Service) agentLocator(id string) string {
	if s.st != nil {
		return "db:agent/" + id
	}
	dir := s.agentDirSnapshot()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, id+".json")
}

// agentSourceFile 某 agent 的定义来源（不存在 → 空 = 内置播种，即"没有载体"）。
//
// 与 `agentLocator` 的区别：它回答的是**载体里到底有没有这一条**（面板的 origin/SourceFile 依据），
// 所以文件模式要 stat、sqlite 模式要查行；种子（无文件/无行）返回空串。
func (s *Service) agentSourceFile(id string) string {
	if s.st != nil {
		if !validDefinitionID(id) {
			return ""
		}
		_, found, err := s.st.GetAgentDefinition(context.Background(), id)
		if err != nil || !found {
			return ""
		}
		return "db:agent/" + id
	}
	dir := s.agentDirSnapshot()
	if dir == "" || !validDefinitionID(id) {
		return ""
	}
	path := filepath.Join(dir, id+".json")
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}

// agentBody 把定义编成载体的字节（memory 写文件 / sqlite 写行共用同一份，保证两侧同形）。
//
// 空字段回填**在调用方完成**（`persistAgentDefinition` 从内存补齐 name/runtimes/provider）——
// 这里保持纯函数，测试可以直接断言字节。
func agentBody(d agentDefinitionFile) (string, error) {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return "", err
	}
	// **不加尾换行**：这与 M6 之前 `persistAgentDef` 写进文件的字节逐字节相同（能力面那边
	// 尾换行来自 `capability.DefinitionFile`，agent 侧从来就没有）。换载体不改文件字节，
	// 才能让"升级前后 diff 为空"这条直觉成立。
	return string(b), nil
}

// createAgentDefinition 新建一条 agent 定义（仅当载体里还没有）。
func (s *Service) createAgentDefinition(ctx context.Context, d agentDefinitionFile) error {
	body, err := agentBody(d)
	if err != nil {
		return err
	}
	if s.st != nil {
		ok, err := s.st.UpsertAgentDefinition(ctx, d.ID, body, 0, definitionWriteActor)
		if err != nil {
			return err
		}
		if !ok {
			return acl.NewError(acl.ClassLimit, "agent_exists",
				fmt.Sprintf("agent %q 的定义已存在（并发新建抢先）", d.ID), nil)
		}
		return nil
	}
	return s.writeAgentFile(d)
}

// updateAgentDefinition 覆盖已存在的载体（**不存在则跳过**——种子没有载体，元数据变更只留内存）。
func (s *Service) updateAgentDefinition(ctx context.Context, d agentDefinitionFile) error {
	if s.st != nil {
		row, found, err := s.st.GetAgentDefinition(ctx, d.ID)
		if err != nil {
			return err
		}
		if !found {
			return nil // 种子/无行：与文件模式"没有定义文件就不落盘"逐字对应
		}
		body, err := agentBody(d)
		if err != nil {
			return err
		}
		ok, err := s.st.UpsertAgentDefinition(ctx, d.ID, body, row.Revision, definitionWriteActor)
		if err != nil {
			return err
		}
		if !ok {
			return definitionChangedErr("db:agent/" + d.ID)
		}
		return nil
	}
	dir := s.agentDirSnapshot()
	if dir == "" {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, d.ID+".json")); err != nil {
		return nil // 无既有文件：种子元数据变更不落 definitions（默认配置即种子）
	}
	return s.writeAgentFile(d)
}

// removeAgentDefinition 删除载体里的定义（种子不可达这里：调用方已挡）。
//
// 返回 false = 载体里本来就没有（文件不存在 / 没有行）——调用方据此保持"删除是幂等的"，
// 但**并发改动**（行 revision 变了）一律报 `definition_changed`：删掉别人刚改过的那一版
// 比覆盖更不可逆。
func (s *Service) removeAgentDefinition(ctx context.Context, id string) (bool, error) {
	if s.st != nil {
		row, found, err := s.st.GetAgentDefinition(ctx, id)
		if err != nil {
			return false, err
		}
		if !found {
			return false, nil
		}
		ok, err := s.st.DeleteAgentDefinition(ctx, id, row.Revision, definitionWriteActor)
		if err != nil {
			return false, err
		}
		if !ok {
			return false, definitionChangedErr("db:agent/" + id)
		}
		return true, nil
	}
	dir := s.agentDirSnapshot()
	if dir == "" {
		return false, nil
	}
	err := os.Remove(filepath.Join(dir, id+".json"))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// writeAgentFile 原子写定义文件（先 tmp 后 rename）——memory 模式的载体实现。
func (s *Service) writeAgentFile(d agentDefinitionFile) error {
	dir := s.agentDirSnapshot()
	if dir == "" {
		return nil // 未启用 definitions 目录 → 仅内存（demo/测试）
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// 空字段回填：文件模式的定义文件是"完整定义"（种子没有文件时不会走到这里，用户 agent 的
	// Name 一定有值；回填是防止 PATCH 只改 provider 时把别的键写成空）。
	if d.Name == "" {
		if a, ok := s.AgentByID(d.ID); ok {
			d.Name = a.Name
		}
	}
	if a, ok := s.AgentByID(d.ID); ok && d.Provider == "" {
		d.Provider = a.Provider
	}
	body, err := agentBody(d)
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+d.ID+".tmp")
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, d.ID+".json"))
}

// ——— 「当前 Agent」的载体（memory = <AgentDir>/default.json；sqlite = meta） ———

// readDefaultAgent 读显式选择（未设置/损坏 → 空串）。
func (s *Service) readDefaultAgent(ctx context.Context) string {
	if s.st != nil {
		id, ok, err := s.st.GetDefaultAgentID(ctx)
		if err != nil {
			glog.Warnw(ctx, "agents: 读 meta.default_agent 失败，回落内置默认", "err", err)
			return ""
		}
		if !ok {
			return ""
		}
		return id
	}
	return readDefaultAgentFile(s.agentDirSnapshot())
}

// writeDefaultAgent 写显式选择。
func (s *Service) writeDefaultAgent(ctx context.Context, id string) error {
	if s.st != nil {
		return s.st.SetDefaultAgentID(ctx, id)
	}
	return writeDefaultAgentFile(s.agentDirSnapshot(), id)
}

// clearDefaultAgent 清除显式选择（当前 Agent 被删后回落内置默认）。
func (s *Service) clearDefaultAgent(ctx context.Context) {
	if s.st != nil {
		if err := s.st.ClearDefaultAgentID(ctx); err != nil {
			glog.Warnw(ctx, "agents: 清除 meta.default_agent 失败", "err", err)
		}
		return
	}
	clearDefaultAgentFile(s.agentDirSnapshot())
}

// loadDefaultAgentFromStore 装载持久层里的显式选择（启动时随定义装载调用）。
//
// 与 `loadDefaultAgentFile` 的语义逐字一致（含"指向已删除/未注册 agent 时本次按回落链生效、
// **不删记录**——定义回来即自动恢复"那条）。
func (s *Service) loadDefaultAgentFromStore(ctx context.Context) {
	id := s.readDefaultAgent(ctx)
	if id == "" {
		return
	}
	s.mu.Lock()
	_, ok := s.agents[id]
	if ok {
		s.currentAgent = id
	}
	s.mu.Unlock()
	if !ok {
		glog.Warnw(ctx, "agents: 当前 Agent 指向未注册 agent，本次回落内置默认", "agent", id)
	}
}
