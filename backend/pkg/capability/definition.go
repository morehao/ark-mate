package capability

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ——— C9：能力定义文件的落盘纪律（唯一实现处） ———
//
// 能力定义是**用户手写的文件**（`<CapabilityDir>/skills/<id>.json`），平台只是偶尔改其中一两个
// 键（启停、迁移、未来的写面）。据此，改文件这件事有四条硬约束，任何一条破掉都会让用户丢数据：
//
//  1. **读成 map，只动目标键**：用 `map[string]json.RawMessage` 承接全文——平台不认识的键
//     原样留在文档里。若读成结构体再写回，所有未知键会在第一次落盘时消失（用户的自定义字段、
//     注释性字段、别的工具写的字段），且是**静默**消失。
//  2. **mtime 乐观锁**：读取时记下 mtime+size，写回前复核。用户可能在面板操作与保存之间
//     手动编辑了文件——此时覆盖写等于把他的手改砸掉。不一致就报错让他重来。
//  3. **同目录 staging + 原子 rename**：半成品绝不能出现在目标路径上（`forge serve` 可能
//     正好在读它）。staging 与目标同目录是必须的——跨设备 rename 不是原子的。
//  4. **保留权限位**：用户可能把定义文件设成 0600（里面可能有内部 URL）。用默认 0644 重建
//     等于悄悄放宽了它的可读范围。
//
// 序列化形态是**确定的**：键按字典序（`json.Marshal` 对 map 的行为）、2 空格缩进、末尾一个换行。
// 因此第一次写回会把用户原来的排版规范化（键序与缩进），此后每次都**逐字节稳定**——幂等性
// 与「--dry-run 输出 == 实跑结果」都建立在这一点上。注意这**不是**逐字节保留原文：值的内层
// 空白也会被重新缩进。语义（含未知键）严格保留，排版会规范化，这条写在测试里。
const definitionIndent = "  "

// DefinitionFile 一个能力定义 JSON 文件的读写句柄（C9 的落点）。
type DefinitionFile struct {
	path string
	// doc 是全文（未知键原样保留的载体）。键序在序列化时排序，故这里用 map 即可。
	doc map[string]json.RawMessage

	// 读取时的文件快照（乐观锁依据）。
	mtime time.Time
	size  int64
	mode  os.FileMode

	// dirty 记录本次是否真的改过键；未改过时 Save 直接跳过（避免无意义的 mtime 变动）。
	dirty bool
}

// ReadDefinitionFile 读取一个定义文件（不存在 → 返回 os.IsNotExist 可判定的错误）。
func ReadDefinitionFile(path string) (*DefinitionFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON 对象：%w", path, err)
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	return &DefinitionFile{path: path, doc: doc, mtime: fi.ModTime(), size: fi.Size(), mode: fi.Mode().Perm()}, nil
}

// Path 定义文件路径。
func (f *DefinitionFile) Path() string { return f.path }

// Has 报告某键是否存在（含值为 null 的键）。
func (f *DefinitionFile) Has(key string) bool {
	_, ok := f.doc[key]
	return ok
}

// Decode 把某个键的原始 JSON 解码到 v（ok=false = 该键不存在）。
// 这是"想读但不想认识它"的读口：读未知键做判断时用它，不必把键加进任何结构体。
func (f *DefinitionFile) Decode(key string, v any) (bool, error) {
	raw, ok := f.doc[key]
	if !ok {
		return false, nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return true, fmt.Errorf("%s 的 %q 字段无法解析：%w", f.path, key, err)
	}
	return true, nil
}

// Set 只改一个键（其余键——含平台不认识的——原样留下）。
// 值序列化后与旧值逐字节相同则不算改动（dirty 不置位，Save 也就不动文件）。
func (f *DefinitionFile) Set(key string, val any) error {
	raw, err := json.Marshal(val)
	if err != nil {
		return fmt.Errorf("序列化 %q 失败：%w", key, err)
	}
	if old, ok := f.doc[key]; ok && sameJSON(old, raw) {
		return nil
	}
	f.doc[key] = raw
	f.dirty = true
	return nil
}

// SetRaw 用原始 JSON 设置一个键（值已经就是 JSON 文本时用）。
func (f *DefinitionFile) SetRaw(key string, raw json.RawMessage) {
	if old, ok := f.doc[key]; ok && sameJSON(old, raw) {
		return
	}
	f.doc[key] = raw
	f.dirty = true
}

// sameJSON 比较两段 JSON 文本是否表示同一个值（**忽略空白**）。
//
// 为什么不直接比字节：读回来的旧值带的是文件里的缩进，而 `json.Marshal(新值)` 是紧凑写法——
// 逐字节比会把"设成同样的值"误判成改动，于是每次 Set 都重写一遍文件（mtime 白白变动，
// "幂等"也就名不副实）。压缩后比较才反映**值**是否真的变了。
func sameJSON(a, b []byte) bool {
	var ca, cb bytes.Buffer
	if err := json.Compact(&ca, a); err != nil {
		return false
	}
	if err := json.Compact(&cb, b); err != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// Delete 摘除一个键（不存在 → false）。
func (f *DefinitionFile) Delete(key string) bool {
	if _, ok := f.doc[key]; !ok {
		return false
	}
	delete(f.doc, key)
	f.dirty = true
	return true
}

// Dirty 报告本次是否改过键。
func (f *DefinitionFile) Dirty() bool { return f.dirty }

// Marshal 返回 Save 将要写入的**确切字节**（键排序 + 2 空格缩进 + 末尾换行）。
// `--dry-run` 与写盘共用它：预览与实际结果不可能不一致。
func (f *DefinitionFile) Marshal() ([]byte, error) {
	out, err := json.MarshalIndent(f.doc, "", definitionIndent)
	if err != nil {
		return nil, fmt.Errorf("序列化 %s 失败：%w", f.path, err)
	}
	return append(out, '\n'), nil
}

// Save 原子写回定义文件（未改过则直接返回，不触碰 mtime）。
//
// 失败时目标文件**一个字节都不变**：staging 写失败、摘要不符、权限设置失败都只清理 staging。
func (f *DefinitionFile) Save() error {
	if !f.dirty {
		return nil
	}
	if err := f.checkUnchanged(); err != nil {
		return err
	}
	out, err := f.Marshal()
	if err != nil {
		return err
	}
	dir := filepath.Dir(f.path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(f.path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("创建临时文件失败 %s：%w", dir, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败 %s：%w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败 %s：%w", tmpName, err)
	}
	// 权限位保留（读不到原权限时退回 0644）：不保留等于把用户设的 0600 悄悄放宽。
	perm := f.mode
	if perm == 0 {
		perm = 0o644
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("设置权限失败 %s：%w", tmpName, err)
	}
	if err := os.Rename(tmpName, f.path); err != nil {
		return fmt.Errorf("替换定义文件失败 %s：%w", f.path, err)
	}
	committed = true
	// 复核后的新快照：同一句柄连续 Save 两次不该因乐观锁自相矛盾。
	if fi, err := os.Stat(f.path); err == nil {
		f.mtime, f.size = fi.ModTime(), fi.Size()
	}
	f.dirty = false
	return nil
}

// checkUnchanged 复核文件自我读取以来没被别人改过（mtime+size 双判：单看 mtime 在同秒内
// 两次修改时会漏判，单看 size 在等长改写时会漏判——两个一起看才够用）。
func (f *DefinitionFile) checkUnchanged() error {
	fi, err := os.Stat(f.path)
	if err != nil {
		return fmt.Errorf("定义文件 %s 已不可读（可能被移走/删除）：%w", f.path, err)
	}
	if fi.ModTime() != f.mtime || fi.Size() != f.size {
		return fmt.Errorf("定义文件 %s 在本次操作期间被外部改动（读取时 mtime=%s size=%d，现在 mtime=%s size=%d）："+
			"为避免覆盖你的手改，本次写入已放弃，请重试",
			f.path, f.mtime.UTC().Format(time.RFC3339Nano), f.size,
			fi.ModTime().UTC().Format(time.RFC3339Nano), fi.Size())
	}
	return nil
}
