package definition

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/morehao/ark-mate/internal/core/capability"
	"github.com/morehao/ark-mate/internal/core/code"
	defval "github.com/morehao/ark-mate/internal/core/definition"
)

// CapabilityDirUnsetErr 未配置能力目录 → 显式报错而不是"内存生效、盘上没留痕"。
//
// 措辞给出路：这是部署配置问题，用户要的是"该设哪个键"，不是一个"写入失败"。
func CapabilityDirUnsetErr(what string) error {
	return code.NewError(code.ClassRequest, "capability_dir_unset",
		fmt.Sprintf("未配置能力目录（MATE_CAPABILITIES），%s只能内存生效、重启即失；设好目录后重试", what), nil)
}

// capabilityDirFor 取某类定义的目录；未配置 → capability_dir_unset。
func (m *Mechanism) CapabilityDirFor(sub string) (string, error) {
	m.mu.Lock()
	dir := m.capabilityDir
	m.mu.Unlock()
	if dir == "" {
		return "", CapabilityDirUnsetErr("能力定义的增删改")
	}
	return filepath.Join(dir, sub), nil
}

// DefinitionDraft 一份"已合并、未落盘"的能力定义（C9 的前两步做完，第三步由调用方决定）。
//
// 拆成 open/save 两段是为了让写面能**先校验再落盘**：注册闸门（正文扫描 / transport 枚举 /
// 密钥引用词法…）必须看到**合并后**的完整定义，而落盘只该发生在校验通过之后。
// 两段仍由同一个句柄串起，所以 mtime 乐观锁覆盖的窗口包含校验全过程——比"读完立刻写"更严。
type DefinitionDraft struct {
	df   *capability.DefinitionFile
	path string
	// existed 打开时文件是否已存在（写面用它判 `*_exists`；也为"新建"分支给出路文案）。
	existed bool
}

// Path 定义文件路径。
func (d *DefinitionDraft) Path() string { return d.path }

// Doc 草稿的文档本体；Existed 打开前该定义是否已存在（CAS 的输入之一）。
// 给**逐类载体**用：automation/provider 的载体要按自己的形状改文档再落盘。
func (d *DefinitionDraft) Doc() *capability.DefinitionFile { return d.df }
func (d *DefinitionDraft) Existed() bool                   { return d.existed }

// Merged 返回合并后的**确切字节**（= 将被写入的内容），供调用方解出强类型视图做校验。
// 与 `--dry-run`、实际落盘共用 `DefinitionFile.Marshal`，三者不可能不一致。
func (d *DefinitionDraft) Merged() ([]byte, error) { return d.df.Marshal() }

// OpenDefinition 读原文件（不存在 → 新建）→ 应用 patch（值 nil = 删键）→ 返回**未落盘**的草稿。
func OpenDefinition(dir, Kind, id string, patch map[string]any) (*DefinitionDraft, error) {
	path, err := defval.DefinitionPath(dir, Kind, id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, code.NewError(code.ClassService, "definition_write_failed",
			fmt.Sprintf("创建能力目录 %s 失败：%v", dir, err), err)
	}
	// 三种"读不了"要分开给路：文件不存在 → 新建；内容不是 JSON 对象 → **用户的文件坏了，
	// 先修它**（40001，可操作）；其余（权限/IO）→ 服务端问题（50000）。混成一个码的话，
	// 用户拿到的是"写入失败"，而真正该做的是去修那个文件。
	df, err := capability.ReadDefinitionFile(path)
	existed := err == nil
	switch {
	case err == nil:
	case errors.Is(err, capability.ErrNotObject):
		return nil, code.NewError(code.ClassRequest, "definition_invalid",
			fmt.Sprintf("能力定义 %s 不是合法 JSON 对象，先修好或删掉它再写：%v", path, err), err)
	case os.IsNotExist(err):
		df, err = capability.NewDefinitionFile(path)
		if err != nil {
			return nil, code.NewError(code.ClassService, "definition_write_failed",
				fmt.Sprintf("准备新建能力定义 %s 失败：%v", path, err), err)
		}
		// 新建路径也要防"我以为是新建、其实文件已被别人建出来"：NewDefinitionFile 的
		// mtime 是零值，Save 的乐观锁一比对就会判冲突（见 capability.NewDefinitionFile）。
	default:
		return nil, code.NewError(code.ClassService, "definition_write_failed",
			fmt.Sprintf("读取能力定义 %s 失败：%v", path, err), err)
	}
	for _, k := range defval.SortedPatchKeys(patch) {
		if patch[k] == nil {
			df.Delete(k)
			continue
		}
		if err := df.Set(k, patch[k]); err != nil {
			return nil, code.NewError(code.ClassService, "definition_write_failed",
				fmt.Sprintf("能力定义 %s 的键 %q 无法序列化：%v", path, k, err), err)
		}
	}
	return &DefinitionDraft{df: df, path: path, existed: existed}, nil
}

// SaveDefinition 落盘草稿（C9 的第四、五步：同目录 staging + 原子 rename + 权限沿用 + mtime 复核）。
// 未改过任何键 → 不触碰文件（幂等）。
func SaveDefinition(d *DefinitionDraft) error {
	if err := d.df.Save(); err != nil {
		return DefinitionSaveErr(d.path, err)
	}
	return nil
}

// WriteDefinition 打开 + 落盘（一步到位）。给不需要"先校验再写"的调用方（automation 运行态 overlay）。
func WriteDefinition(dir, Kind, id string, patch map[string]any) error {
	if len(patch) == 0 {
		return nil // 没有要改的键：不写盘（避免"什么都没改却换了 mtime"）
	}
	d, err := OpenDefinition(dir, Kind, id, patch)
	if err != nil {
		return err
	}
	return SaveDefinition(d)
}

// RemoveDefinitionPermanently 删掉 `dir/<id>.json`；文件本来就不存在 → (false, nil)（幂等删除）。
func RemoveDefinitionPermanently(dir, Kind, id string) (bool, error) {
	path, err := defval.DefinitionPath(dir, Kind, id)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, code.NewError(code.ClassService, "definition_delete_failed",
			fmt.Sprintf("删除能力定义 %s 失败：%v", path, err), err)
	}
	return true, nil
}

// DefinitionSaveErr 把落盘失败映射成对外语义：
//
//   - 乐观锁冲突 → `definition_changed`（40901）：**请刷新重试**，你的改动没写入（绝不 last-write-wins）；
//   - 其余 IO 失败 → `definition_write_failed`（50000）：服务端出错了，用户报障而不是重试。
//
// 抽成函数是为了让这条映射可被单测直接钉住（冲突本身由 `pkg/capability` 用真实文件改动验证）。
func DefinitionSaveErr(path string, err error) error {
	if capability.IsConflict(err) {
		// ClassLimit 是"与当前状态冲突"这一类（→ 40901）。用 ClassRequest 会落成 40001
		// 「请求非法」，用户看到的是"我的参数错了"，而实际动作是"刷新重试"。
		return code.NewError(code.ClassLimit, "definition_changed",
			fmt.Sprintf("能力定义 %s 在本次编辑期间被外部改动，请刷新后重试（你的改动未写入）", path), err)
	}
	return code.NewError(code.ClassService, "definition_write_failed",
		fmt.Sprintf("写入能力定义 %s 失败：%v", path, err), err)
}
