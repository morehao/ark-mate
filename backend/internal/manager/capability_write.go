package manager

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-forge/pkg/acl"
	"github.com/morehao/ark-forge/pkg/capability"
)

// ——— 能力定义写面：C9 落盘纪律的唯一实现 ———
//
// 能力定义的**事实源永远是文件**（`<CapabilityDir>/{skills,mcp,tools}/<id>.json`），面板只是
// 编辑器。所以写面只做一件事：把"改哪几个键"落进文件，其余字节不碰。
//
// 这里封死 C9 的五条（见 AGENTS.md 与本计划 §0）：
//
//  1. 读原文件为 `map[string]any`（不存在 = 空 map → 新建）；
//  2. **只改 patch 里的键**（patch 值为 nil = 删掉该键）；
//  3. **未知键原样保留**——用户手写的注释性字段、未来版本新增的键，都不该被一次编辑抹掉；
//  4. 同目录 staging + 原子 `rename` + **权限沿用**原文件（`capability.DefinitionFile`）；
//  5. 写前比对 mtime：读→写窗口内被外部改动 → `definition_changed`，**绝不 last-write-wins**
//     （风险表第 3 行）。
//
// 一个必须说清的边界：第 5 条防的是**并发写**（两个 PUT / PUT 撞上手工编辑 / 撞上
// `forge capability migrate`），不是"客户端 GET 之后有人改了文件"。后者不需要报错也不该报错
// ——PUT 的语义是"这几个键置成这些值"，而未知键在第 1 步就被重新读入，所以别人在你 GET 之后
// 写的**别的键**不会丢。会丢的只有"你们改了同一个键"，那时后写者赢是 PUT 的应有语义。

// capabilitySubdir 三类能力定义各自的子目录名（`<CapabilityDir>/<subdir>/<id>.json`）。
const (
	capSubSkills = "skills"
	capSubMcps   = "mcp"
)

// capabilityDirUnsetErr 未配置能力目录 → 显式报错而不是"内存生效、盘上没留痕"。
//
// 措辞给出路：这是部署配置问题，用户要的是"该设哪个键"，不是一个"写入失败"。
func capabilityDirUnsetErr(what string) error {
	return acl.NewError(acl.ClassRequest, "capability_dir_unset",
		fmt.Sprintf("未配置能力目录（FORGE_CAPABILITIES），%s只能内存生效、重启即失；设好目录后重试", what), nil)
}

// capabilityDirFor 取某类定义的目录；未配置 → capability_dir_unset。
func (s *Service) capabilityDirFor(sub string) (string, error) {
	s.mu.Lock()
	dir := s.capabilityDir
	s.mu.Unlock()
	if dir == "" {
		return "", capabilityDirUnsetErr("能力定义的增删改")
	}
	return filepath.Join(dir, sub), nil
}

// validDefinitionID 定义 id 可否作为文件名。
//
// **这是一条安全边界，不只是格式校验**：id 来自 URL 路径与 body，直接拼进路径就成了目录穿越
// （`PUT /skills/..%2F..%2Fetc%2Fpasswd`）。上游 `RegisterSkill`/`RegisterMcpServer` 已经按
// 同一套规则（`nameRE`）拦过一道，但 `writeDefinition` 也会被删除/迁移路径调用，所以在这里
// 再判一次——**最后一道防线放在真正做路径拼接的地方**。
func validDefinitionID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	if id == "." || id == ".." || strings.ContainsAny(id, `/\`) {
		return false
	}
	if filepath.Base(id) != id || id != filepath.Clean(id) {
		return false
	}
	return true
}

// invalidDefinitionIDErr 非法 id 的错误（文案不回显原始字符串的长度细节，只说明规则）。
func invalidDefinitionIDErr(kind, id string) error {
	return acl.NewError(acl.ClassRequest, "invalid_param",
		fmt.Sprintf("%s id %q 不能作为文件名（须字母数字开头、≤64 位、不含路径分隔符）", kind, id), nil)
}

// definitionPath 拼出 `dir/<id>.json`；id 非法即报错（见 validDefinitionID）。
func definitionPath(dir, kind, id string) (string, error) {
	if !validDefinitionID(id) {
		return "", invalidDefinitionIDErr(kind, id)
	}
	return filepath.Join(dir, id+".json"), nil
}

// writeDefinition 把 patch 落进 `dir/<id>.json`（C9）。
//
// patch 的键 = 要改的键；**值为 nil = 删除该键**。这不是"完整覆盖"语义——调用方（写面）负责
// 把请求体翻译成 patch，未出现的键一律不动。
func writeDefinition(dir, kind, id string, patch map[string]any) error {
	if len(patch) == 0 {
		return nil // 没有要改的键：不写盘（避免"什么都没改却换了 mtime"）
	}
	path, err := definitionPath(dir, kind, id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("创建能力目录 %s 失败：%v", dir, err), err)
	}
	// 三种"读不了"要分开给路：文件不存在 → 新建；内容不是 JSON 对象 → **用户的文件坏了，
	// 先修它**（40001，可操作）；其余（权限/IO）→ 服务端问题（50000）。混成一个码的话，
	// 用户拿到的是"写入失败"，而真正该做的是去修那个文件。
	df, err := capability.ReadDefinitionFile(path)
	switch {
	case err == nil:
	case errors.Is(err, capability.ErrNotObject):
		return acl.NewError(acl.ClassRequest, "definition_invalid",
			fmt.Sprintf("能力定义 %s 不是合法 JSON 对象，先修好或删掉它再写：%v", path, err), err)
	case os.IsNotExist(err):
		df, err = capability.NewDefinitionFile(path)
		if err != nil {
			return acl.NewError(acl.ClassService, "definition_write_failed",
				fmt.Sprintf("准备新建能力定义 %s 失败：%v", path, err), err)
		}
		// 新建路径也要防"我以为是新建、其实文件已被别人建出来"：NewDefinitionFile 的
		// mtime 是零值，Save 的乐观锁一比对就会判冲突（见 capability.NewDefinitionFile）。
	default:
		return acl.NewError(acl.ClassService, "definition_write_failed",
			fmt.Sprintf("读取能力定义 %s 失败：%v", path, err), err)
	}
	for _, k := range sortedPatchKeys(patch) {
		if patch[k] == nil {
			df.Delete(k)
			continue
		}
		if err := df.Set(k, patch[k]); err != nil {
			return acl.NewError(acl.ClassService, "definition_write_failed",
				fmt.Sprintf("能力定义 %s 的键 %q 无法序列化：%v", path, k, err), err)
		}
	}
	if !df.Dirty() {
		return nil // 同值重复设置：不脏不写盘（幂等，见 DefinitionFile）
	}
	if err := df.Save(); err != nil {
		return definitionSaveErr(path, err)
	}
	return nil
}

// definitionSaveErr 把落盘失败映射成对外语义：
//
//   - 乐观锁冲突 → `definition_changed`（40901）：**请刷新重试**，你的改动没写入（绝不 last-write-wins）；
//   - 其余 IO 失败 → `definition_write_failed`（50000）：服务端出错了，用户报障而不是重试。
//
// 抽成函数是为了让这条映射可被单测直接钉住（冲突本身由 `pkg/capability` 用真实文件改动验证）。
func definitionSaveErr(path string, err error) error {
	if capability.IsConflict(err) {
		// ClassLimit 是"与当前状态冲突"这一类（→ 40901）。用 ClassRequest 会落成 40001
		// 「请求非法」，用户看到的是"我的参数错了"，而实际动作是"刷新重试"。
		return acl.NewError(acl.ClassLimit, "definition_changed",
			fmt.Sprintf("能力定义 %s 在本次编辑期间被外部改动，请刷新后重试（你的改动未写入）", path), err)
	}
	return acl.NewError(acl.ClassService, "definition_write_failed",
		fmt.Sprintf("写入能力定义 %s 失败：%v", path, err), err)
}

// removeDefinition 删掉 `dir/<id>.json`；文件本来就不存在 → (false, nil)（幂等删除）。
func removeDefinition(dir, kind, id string) (bool, error) {
	path, err := definitionPath(dir, kind, id)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, acl.NewError(acl.ClassService, "definition_delete_failed",
			fmt.Sprintf("删除能力定义 %s 失败：%v", path, err), err)
	}
	return true, nil
}

// sortedPatchKeys patch 的键按字典序（落盘顺序稳定 → 同一请求产物逐字可复现，也方便单测比对）。
// 顺序本身不影响语义（DefinitionFile 内部按 key 排序序列化），这里只是让"先删后设"的意图可读。
func sortedPatchKeys(patch map[string]any) []string {
	out := make([]string, 0, len(patch))
	for k := range patch {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
