package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/dto/dtoagent"
	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/golib/glog"
)

// ——— 当前 Agent（全局默认）切换：<AgentDir>/default.json ———
//
// 语义：**当前 Agent 是一个全局单例选择**（不是 agent 自身的属性）。未显式指定 agent_id 的
// 动作都取它——新建会话（resolveConversationAgent）、模型目录缺省（GET /models）、mate demo。
// 回落链：显式选择（agent 仍存在）> 内置种子 dsh > id 升序首个；注册表为空才报错。
//
// 历史：默认 Agent 曾是硬编码常量（永远是 dsh），面板上「切换 agent」无从落地；现由用户经
// `POST /agents/:id/default` 切换，选择落 <AgentDir>/default.json 以便重启保持。
// 该文件与 providers.json 同属**目录级配置**，不是 agent 定义（LoadAgentDefinitions 跳过）。

// defaultAgentFile 当前 Agent 的落盘文件名（位于 definitions 目录下）。
const defaultAgentFile = "default.json"

// defaultAgentDoc <AgentDir>/default.json 的结构。
type defaultAgentDoc struct {
	// DefaultAgent 当前 Agent 的 id（空 = 未显式设置，按回落链推导）。
	DefaultAgent string `json:"default_agent"`
}

// readDefaultAgentFile 读 <AgentDir>/default.json（缺失/损坏 → 空串 = 未显式设置）。
func readDefaultAgentFile(dir string) string {
	if dir == "" {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, defaultAgentFile))
	if err != nil {
		return "" // 首次/从未切换 → 走回落链
	}
	var d defaultAgentDoc
	if err := json.Unmarshal(b, &d); err != nil {
		glog.Warnw(context.Background(), "agents: default.json 非法，回落内置默认", "dir", dir, "err", err)
		return ""
	}
	return d.DefaultAgent
}

// loadDefaultAgentFile 装载显式选择（<AgentDir>/default.json，启动时随 definitions 调用）。
// 指向已删除/未注册的 agent 时本次按回落链生效（不删文件：definition 回来即自动恢复）。
func (s *Service) loadDefaultAgentFile(dir string) {
	id := readDefaultAgentFile(dir)
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
		glog.Warnw(context.Background(), "agents: default.json 指向未注册 agent，本次回落内置默认", "agent", id)
	}
}

// writeDefaultAgentFile 原子写 <AgentDir>/default.json（dir 空 = 仅内存，不落盘）。
func writeDefaultAgentFile(dir, id string) error {
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(defaultAgentDoc{DefaultAgent: id}, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+defaultAgentFile+".tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, defaultAgentFile))
}

// clearDefaultAgentFile 删除 <AgentDir>/default.json（当前 Agent 被删后回到回落链）。
func clearDefaultAgentFile(dir string) {
	if dir == "" {
		return
	}
	if err := os.Remove(filepath.Join(dir, defaultAgentFile)); err != nil && !os.IsNotExist(err) {
		glog.Warnw(context.Background(), "agents: 清理 default.json 失败", "dir", dir, "err", err)
	}
}

// SetDefaultAgent 切换「当前 Agent」（全局默认：新建会话/模型目录缺省取它；幂等）。
// 未知 agent 报 agent_not_found；选择落**载体**（memory = `<AgentDir>/default.json`，
// sqlite = `meta.default_agent`），重启后保持。
func (s *Service) SetDefaultAgent(ctx context.Context, id string) (*dtoagent.AgentResp, error) {
	s.mu.Lock()
	if _, ok := s.agents[id]; !ok {
		s.mu.Unlock()
		return nil, acl.NewError(acl.ClassRequest, "agent_not_found", fmt.Sprintf("未知 agent %q", id), nil)
	}
	s.currentAgent = id
	s.mu.Unlock()
	if err := s.writeDefaultAgent(ctx, id); err != nil {
		return nil, err
	}
	glog.Infow(ctx, "agent default switched", "agent", id)
	return s.agentRespLocked(id), nil
}
