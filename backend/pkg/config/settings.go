package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// SettingsStore 是 manager.WorkspaceRootsStore 的 infra 实现：让面板把工作区根白名单写回
// `<配置目录>/config.json`，并把「该键被更高优先级来源锁定」这一事实显式报给上层。
//
// 为什么不另开一份 settings.json：白名单只应有一个事实源。真正的风险也不是"写哪个文件"，
// 而是**面板写了却不生效**——配置优先级是 默认值 < config.json < env 文件 < 进程 `FORGE_*` < flag，
// 高优先级来源存在时写 config.json 必然被覆盖。所以这里不隐藏优先级，而是把它报出来（Status），
// 由上层拒绝写并让面板转只读。
type SettingsStore struct {
	// dir 配置目录（空 = 不可写：没有配置目录的进程，如 acp-serve/demo，不该落盘）。
	dir string
	// src workspace_roots 键的最终来源（来自 LoadResolved 的 Sources；空 = 任何来源都没设过）。
	src Source
}

// NewSettingsStore 构造白名单持久化端口。src 传 Sources["workspace_roots"] 即可。
func NewSettingsStore(dir string, src Source) *SettingsStore {
	return &SettingsStore{dir: dir, src: src}
}

// SaveWorkspaceRoots 把白名单写回 config.json（read-modify-write，保留文件里的其它键）。
//
// 写数组形式（不是逗号分隔串）：路径里含逗号时只有数组能原样往返，而写回格式与读回格式
// 必须自洽，否则面板保存一次就把运维的某个路径劈成两条。
func (s *SettingsStore) SaveWorkspaceRoots(roots []string) error {
	if strings.TrimSpace(s.dir) == "" {
		return fmt.Errorf("未解析到配置目录")
	}
	path := filepath.Join(s.dir, "config.json")
	obj := map[string]any{}
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		if jerr := json.Unmarshal(raw, &obj); jerr != nil {
			// 不在这里"顺手修好"坏文件：它会让下次启动直接失败，替运维决定等于把问题藏起来。
			return fmt.Errorf("%s 不是合法 JSON 对象: %w", path, jerr)
		}
	case os.IsNotExist(err):
	default:
		return err
	}
	if len(roots) == 0 {
		// 空 = 不限制：删键而不是写空数组，避免"显式空"与"未设置"两种写法在文件里并存。
		delete(obj, "workspace_roots")
	} else {
		obj["workspace_roots"] = roots
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return err
	}
	mode := os.FileMode(0o600) // 配置目录里可能有 FORGE_TOKEN：默认不给同组/其他用户读
	if st, serr := os.Stat(path); serr == nil {
		mode = st.Mode().Perm() // 已有文件保留原权限（不借写回之机改运维的设置）
	}
	buf, err := json.MarshalIndent(obj, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(buf, '\n'), mode); err != nil {
		return err
	}
	// 原子替换：配置文件写到一半等于下次启动直接失败。
	return os.Rename(tmp, path)
}

// Status 返回该键的最终来源与锁定情况；lockedBy 非空 = 写入必被该来源覆盖，故面板只读。
//
// 锁定的三种来源都是"比配置文件更高优先级"的配置面，这里给出的都是运维可直接去改的那个位置
// （env 变量名 / env 文件路径），而不是抽象的 "env(process)" —— 报错要能直接指向下一步动作。
func (s *SettingsStore) Status() (source, lockedBy string) {
	src := s.src
	if src == "" {
		src = SourceDefault
	}
	switch src {
	case SourceDefault, SourceFile:
		return string(src), ""
	case SourceEnv:
		return string(src), "FORGE_WORKSPACE_ROOTS"
	case SourceEnvFile:
		return string(src), filepath.Join(s.dir, "env")
	default: // SourceFlag：CLI flag 高于一切配置面
		return string(src), "命令行参数"
	}
}
