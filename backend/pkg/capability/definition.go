package capability

import (
	"bytes"
	"encoding/json"
	"errors"
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

// ErrNotObject 文件内容不是 JSON 对象（数组/标量/语法错误）。
//
// 单独成哨兵错误是为了让上层**按语义**分流：这是"用户文件坏了，先修它"（40001），
// 与"服务端读不了"（权限/IO，50000）是两种不同的出路。用字符串匹配错误信息来分流是脆的。
var ErrNotObject = errors.New("不是 JSON 对象")

// DefinitionFile 一个能力定义 JSON 文件的读写句柄（C9 的落点）。
type DefinitionFile struct {
	path string
	// doc 是全文（未知键原样保留的载体）。键序在序列化时排序，故这里用 map 即可。
	doc map[string]json.RawMessage

	// 读取时的文件快照（乐观锁依据）。
	mtime time.Time
	size  int64
	mode  os.FileMode
	// existed 读取时文件是否存在。**新建路径必须记下这件事**：文件"不存在"与"存在但零字节"
	// 是两种不同的期望，不区分的话新建时的 checkUnchanged 会把"文件还不存在"当成"被别人删了"，
	// 从而永远写不出第一个文件（而错误信息看起来像并发冲突，极难定位）。
	existed bool

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
		return nil, fmt.Errorf("%s 不是合法 JSON 对象：%w（%w）", path, err, ErrNotObject)
	}
	if doc == nil {
		// 合法 JSON 但不是对象（数组/字符串/数字）：当成"不是定义文件"而不是空对象——
		// 静默当空对象会让写面把它整个覆盖掉，而它很可能正是用户想留着的别的东西。
		return nil, fmt.Errorf("%s 的内容不是 JSON 对象，无法作为能力定义读写：%w", path, ErrNotObject)
	}
	return &DefinitionFile{path: path, doc: doc, mtime: fi.ModTime(), size: fi.Size(),
		mode: fi.Mode().Perm(), existed: true}, nil
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
	f.existed = true
	f.dirty = false
	return nil
}

// NewDefinitionFile 造一个**空**定义句柄（目标文件尚不存在的新建路径）。
//
// 它对"文件已存在"是安全的：Save 的乐观锁会把 mtime=零值 与实际 mtime 一比即不相等，
// 从而拒绝覆盖——这正是我们要的（新建路径发现文件已被别人建出来，应当报冲突而不是合并）。
// 所以调用方若在"文件不存在"与"新建"之间看到错误，不该自己 stat 一把再决定，直接用本函数。
func NewDefinitionFile(path string) (*DefinitionFile, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("定义文件路径须为绝对路径：%s", path)
	}
	return &DefinitionFile{path: path, doc: map[string]json.RawMessage{}, mode: 0o644, existed: false}, nil
}

// NewDefinitionDoc 造一个**内存里**的空定义文档（sqlite 载体的新建路径，M6-3）。
//
// 与 NewDefinitionFile 的差别只有一处：不做绝对路径校验（DB 行没有文件路径；`path` 只是
// 人读的定位串）。落盘纪律里与文件相关的那三条（staging/原子 rename/权限沿用/mtime 乐观锁）
// 对 DB 载体不适用——DB 侧的乐观锁是行 revision，由 store 的事务承担。
func NewDefinitionDoc(path string) *DefinitionFile {
	return &DefinitionFile{path: path, doc: map[string]json.RawMessage{}, mode: 0o644, existed: false}
}

// DecodeDefinitionDoc 用一段定义正文造**内存里**的文档句柄（sqlite 载体的读口，M6-3）。
//
// 与 ReadDefinitionFile 共用同一套语义：读成 `map[string]json.RawMessage`（未知键原样保留）、
// 非法 JSON 或"合法 JSON 但不是对象"都报 ErrNotObject（上层据此给"先修它"的 40001）。
func DecodeDefinitionDoc(path string, raw []byte) (*DefinitionFile, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s 不是合法 JSON 对象：%w（%w）", path, err, ErrNotObject)
	}
	if doc == nil {
		return nil, fmt.Errorf("%s 的内容不是 JSON 对象，无法作为能力定义读写：%w", path, ErrNotObject)
	}
	return &DefinitionFile{path: path, doc: doc, mode: 0o644, existed: true}, nil
}

// WriteDefinitionFile 把一段定义正文**覆盖写**到 path（M6-4 的导出路径用）。
//
// 与 `Save` 的分工：`Save` 是"改我读过的那份文件的几个键"——带 mtime 乐观锁、保留未知键与权限，
// 面向"用户在编辑定义"；导出是"让文件变成 DB 里那一版"：正文已经由 DB 决定（没有"未知键"要保护，
// 因为在 DB 的那份 body 里它们已经被保留），且**必须能覆盖已存在的文件**——否则第二次导出就会
// 以"冲突"告终，而导出本来就没有并发语义可言。
//
// 复用 C9 里与内容无关的两条纪律：**同目录 staging + 原子 rename**（半成品绝不出现在目标路径上）
// 与**权限沿用**（文件已存在时用它的权限位，不因一次导出把 0600 放宽成 0644）。
func WriteDefinitionFile(path string, body []byte) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("定义文件路径须为绝对路径：%s", path)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("创建能力目录失败 %s：%w", dir, err)
	}
	mode := os.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
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
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return fmt.Errorf("写入临时文件失败 %s：%w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败 %s：%w", tmpName, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("设置权限失败 %s：%w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("替换定义文件失败 %s：%w", path, err)
	}
	committed = true
	return nil
}

// ConflictError 定义文件在"读→写"窗口内被外部改动（乐观锁拒绝）。
//
// 单独成类型是为了让上层能**按语义**分流：冲突是"请重试"（40901 / definition_changed），
// 而 IO 失败是"服务端出错了"（50000）。用字符串匹配错误信息来分流是脆的——文案一改分流就错。
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// IsConflict 报告 err 是否是乐观锁冲突（errors.As 语义，可穿透包装）。
func IsConflict(err error) bool {
	var ce *ConflictError
	return errors.As(err, &ce)
}

// checkUnchanged 复核文件自我读取以来没被别人改过（mtime+size 双判：单看 mtime 在同秒内
// 两次修改时会漏判，单看 size 在等长改写时会漏判——两个一起看才够用）。
func (f *DefinitionFile) checkUnchanged() error {
	fi, err := os.Stat(f.path)
	if err != nil {
		if !f.existed && os.IsNotExist(err) {
			return nil // 新建路径：文件本就不该在（仍不在 = 期望成立）
		}
		return fmt.Errorf("定义文件 %s 已不可读（可能被移走/删除）：%w", f.path, err)
	}
	if !f.existed {
		// 我以为是新建，但文件已经存在（另一个进程/另一次请求刚建出来）：冲突，不覆盖。
		return &ConflictError{Msg: fmt.Sprintf(
			"定义文件 %s 在本次新建期间已存在（mtime=%s size=%d）：为避免覆盖别人的写入，本次新建已放弃",
			f.path, fi.ModTime().UTC().Format(time.RFC3339Nano), fi.Size())}
	}
	if fi.ModTime() != f.mtime || fi.Size() != f.size {
		return &ConflictError{Msg: fmt.Sprintf("定义文件 %s 在本次操作期间被外部改动（读取时 mtime=%s size=%d，现在 mtime=%s size=%d）："+
			"为避免覆盖你的手改，本次写入已放弃，请重试",
			f.path, f.mtime.UTC().Format(time.RFC3339Nano), f.size,
			fi.ModTime().UTC().Format(time.RFC3339Nano), fi.Size())}
	}
	return nil
}
