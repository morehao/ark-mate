// Package credentials 提供「凭据从哪儿来」的**唯一实现**（决策 v20）：
// 一份**严格映射**的托管文档（`<MATE_HOME>/credentials.json`，0600）+ 四层回落。
//
// 分层顺序是**平台自己的部署契约**（不是某个底座实现的镜像）：
//
//		继承进程环境（只读，最高） > 托管文档（可写） > <cwd>/.env（只读） > <home>/.env（只读）
//
//	  - **继承环境最高**：`DEEPSEEK_API_KEY=… mate serve` / CI secret / 容器 `-e` 表达的是「这次运行的
//	    显式意图」，被静默遮蔽是最坏的后果（人以为换了密钥，其实没换）。
//	  - **托管文档高于 `.env`**：面板上写的密钥不能被一份陈旧 `.env` 顶掉。
//	  - **项目级高于用户级**：更具体的位置赢。
//
// **与 dsh 的关系（2026-10-18 修订）**：0.2.0 时 dsh 的 `credentials-local` 也是 env 优先，
// 本包照它对齐；0.2.1 起 dsh 反转为「存储文档 > 继承环境」。平台**不跟随**：平台是 dsh 原生
// 文档的唯一写者（只写 `MATE_*` ref），两套层序不会在交付面上打架；跟随反而会让用户导出的
// 环境变量突然输给面板里的旧密钥。详见 `docs/dsh-acp-adaptation.md` §4。
//
// 为什么只有这一处实现：顺序散着写必然漂移，而漂移的表现是**静默**的——"面板说已设置、生效的是
// 另一把"。这不是假想：平台侧的 `has_key` 与「设置 · 模型」曾对层序一无所知（2026-10-14 核实）。
//
// 三个刻意选择的语义，都在下面各方法的注释里复述了一遍：
//
//  1. **一层读不了就停，不用更低的层**（`Resolve`）：读不了意味着"这一层有没有那把钥匙"未知，
//     继续往下走会把"配置坏了"悄悄变成"换了把钥匙"。文件**不存在**不算读不了。
//  2. **托管文档权限过宽 = 硬失败**（`Doc.Load`）：它与 dsh 拒绝加载可被属主之外读取的凭据文档
//     是同一口径；`Doc.Save` 则以 0600 原子重写（把走宽的权限一并治好）。
//  3. **托管文档是严格 `引用 → 值` 映射，刻意不是 dotenv**（`Doc.Load`）：文件形状坏掉时要能
//     一眼看出，而不是让一个半吊子 env 解析器把 `{"a":1}` 当成"没有密钥"。
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/morehao/ark-mate/internal/credential"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Request 描述"要解析哪个凭据"。
//
// 两个字段各有出处，不能合并：**引用**是托管文档里的键（形如 `providers/<id>`、`store:<ns>/<name>`），
// 而**环境变量名**是 `.env` 与继承环境这两层的键（形如 `<TYPE>_API_KEY`）。这两层的键空间天然不同
// （托管文档按"我们的引用"命名，dotenv 按"底座的变量名"命名），硬合成一个键会让其中一侧永远是死的。
type Request struct {
	// Ref 托管文档里的键。空 = 这一层不参与（例如没有持久层时）。
	Ref string
	// EnvKeys 候选环境变量名，按优先级排列（通常只有一个）。
	EnvKeys []string
}

// Resolution 是一次解析的结果。`Found=false` 与 `Problems` 非空**是两件事**：
// 前者是"四层里都没有"，后者是"有一层读不了"——把它们混成一句"没有密钥"，正是本包要消灭的假绿。
type Resolution struct {
	Value    string
	Source   credential.Source
	Found    bool
	Problems []string
}

// Layered 把四层合成一个查询面。字段是导出值类型，便于装配处直接构造（本包不做全局状态：
// 解析面必须能被测试逐个实例化，否则"四层顺序"这条判据就只能靠手测）。
type Layered struct {
	// Managed 托管文档；nil = 没有持久层（memory 模式）或未配置。
	Managed *Doc
	// CwdFile / HomeFile 两份 dotenv 的路径；空 = 该层不参与。
	CwdFile  string
	HomeFile string
	// Env 取继承环境变量的函数；nil = `os.Getenv`。注入点是为了让"四层顺序"可测。
	Env func(string) string
}

// New 按约定位置装配四层：`<cwdDir>/.env` 与 `<homeDir>/.env`；`managed` 可为 nil。
func New(managed *Doc, cwdDir, homeDir string) *Layered {
	l := &Layered{Managed: managed}
	if strings.TrimSpace(cwdDir) != "" {
		l.CwdFile = filepath.Join(cwdDir, ".env")
	}
	if strings.TrimSpace(homeDir) != "" {
		l.HomeFile = filepath.Join(homeDir, ".env")
	}
	return l
}

// Resolve 按四层顺序找一把钥匙。
//
// 语义（每一条都有判据）：
//   - 空串不算命中（与既有代码一致：`if v := os.Getenv(name); v != ""`）——把空值当"已设置"会让
//     底座拿着空密钥去换 401，而错报在很远的地方。
//   - 托管文档**存在但读不了**（权限过宽/形状坏掉）→ 当场停，**不**回落到 `.env`：回落会换成
//     另一把钥匙，而用户以为用的是托管的那把。
//   - 任一 dotenv 层**存在但读不了**→ 同样停（理由同上：这一层有没有那把钥匙未知）。
//   - 文件**不存在**不是问题（绝大多数机器上没有 `.env`），只在真的读不了时记 `Problems`。
func (l *Layered) Resolve(req Request) Resolution {
	if l == nil {
		return Resolution{Problems: []string{"凭据解析器未配置"}}
	}
	env := l.Env
	if env == nil {
		env = os.Getenv
	}
	// ① 继承环境。
	for _, k := range req.EnvKeys {
		if strings.TrimSpace(k) == "" {
			continue
		}
		if v := env(k); strings.TrimSpace(v) != "" {
			return Resolution{Value: v, Source: credential.SourceEnv, Found: true}
		}
	}
	// ② 托管文档。
	if l.Managed != nil && l.Managed.Path() != "" {
		values, err := l.Managed.Load()
		if err != nil {
			return Resolution{Problems: []string{"托管凭据文档不可用（不再回落到 .env）：" + err.Error()}}
		}
		if v, ok := values[req.Ref]; ok && strings.TrimSpace(v) != "" {
			return Resolution{Value: v, Source: credential.SourceManaged, Found: true}
		}
	}
	// ③④ 两份 dotenv（同形，只有来源不同；项目级在前）。
	var problems []string
	layers := []struct {
		path string
		src  credential.Source
	}{{l.CwdFile, credential.SourceDotenvCwd}, {l.HomeFile, credential.SourceDotenvHome}}
	for _, layer := range layers {
		if strings.TrimSpace(layer.path) == "" {
			continue
		}
		values, err := readDotenv(layer.path)
		if err != nil {
			problems = append(problems, layer.path+"："+err.Error())
			break
		}
		for _, k := range req.EnvKeys {
			if strings.TrimSpace(k) == "" {
				continue
			}
			if v, ok := values[k]; ok && strings.TrimSpace(v) != "" {
				return Resolution{Value: v, Source: layer.src, Found: true}
			}
		}
	}
	return Resolution{Problems: problems}
}

// readDotenv 读一份 dotenv。**刻意只支持一个很小的子集**（解析器越大，它自己的出错面越大：
// 我们需要的只是"把 `KEY=VALUE` 读进来"，不是复刻 shell）：
//
//	KEY=VALUE / export KEY=VALUE / 空行 / `#` 注释行；值两侧的单双引号剥掉；
//	双引号内识别 \n \t \\ \" 四条转义；**不做变量展开**（值里出现 `${` 直接报错，见下）；
//	同一个键出现多次时后者胜。
//
// 文件不存在 → (nil, nil)：这是最常见的正常情况，不是问题。
// 任何一行不合法 → 报错（由调用方记成 Problem 并停止往下找，理由见 Resolve）。
func readDotenv(path string) (map[string]string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string]string)
	for i, raw := range strings.Split(string(b), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(raw, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if rest := strings.TrimPrefix(line, "export "); rest != line {
			line = strings.TrimSpace(rest)
		}
		eq := strings.Index(line, "=")
		if eq <= 0 {
			return nil, fmt.Errorf("第 %d 行不是 KEY=VALUE（不做变量展开、不支持裸值）：%q", i+1, line)
		}
		key := strings.TrimSpace(line[:eq])
		if !validEnvKey(key) {
			return nil, fmt.Errorf("第 %d 行的键名非法：%q", i+1, key)
		}
		value := strings.TrimSpace(line[eq+1:])
		// `${VAR}` 这类**必须报错**，不能当字面量收下：用户写它是想展开，收下就会得到一个字面的
		// `${VAR}` 密钥，症状推迟到底座那边的 401——正是本仓最忌讳的"错在很远的地方"。
		if strings.Contains(value, "${") {
			return nil, fmt.Errorf("第 %d 行的值含 `${…}`：本文件**不做变量展开**（要么写字面量，要么改用进程环境）", i+1)
		}
		out[key] = unquoteEnvValue(value)
	}
	return out, nil
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i := 0; i < len(k); i++ {
		c := k[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case c >= '0' && c <= '9':
			if i == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func unquoteEnvValue(v string) string {
	if len(v) < 2 {
		return v
	}
	if v[0] == '\'' && v[len(v)-1] == '\'' {
		return v[1 : len(v)-1]
	}
	if v[0] == '"' && v[len(v)-1] == '"' {
		inner := v[1 : len(v)-1]
		inner = strings.ReplaceAll(inner, `\n`, "\n")
		inner = strings.ReplaceAll(inner, `\t`, "\t")
		inner = strings.ReplaceAll(inner, `\\`, `\`)
		inner = strings.ReplaceAll(inner, `\"`, `"`)
		return inner
	}
	return v
}

// Doc 是托管凭据文档（`<MATE_HOME>/credentials.json`）。
//
// 形状是**严格 `引用 → 字符串值` 的 JSON 对象**：`{"providers/<id>": "<key>", "store:<ns>/<name>": "<secret>"}`。
// 刻意不是 dotenv：dotenv 没有"值必须是字符串""键必须互不重复"这些结构性保证，形状坏掉时会退化成
// "某些键静默消失"，而这份文件是**密钥的权威面**。
//
// 它是**可重建的镜像**：DB 是权威（决策 v20③）。所以写失败只 WARN + 记 `problems[]`（由调用方决定），
// 而"文件里有、库里没有"**不自动回灌**（否则用户在面板上清掉的密钥会被一份陈旧文件悄悄撤销）。
type Doc struct{ path string }

// Open 绑定一份托管文档的路径。**不读盘**——读盘发生在 Load/Save，于是"打开"不会因为文件暂时
// 读不了而让装配期失败（体检与诊断要能在"文件坏了"的机器上跑起来并把坏法说出来）。
func Open(path string) (*Doc, error) {
	p := strings.TrimSpace(path)
	if p == "" {
		return nil, errors.New("credentials: 托管文档路径为空")
	}
	return &Doc{path: p}, nil
}

// Path 返回托管文档路径（nil 安全：装配期可能没配）。
func (d *Doc) Path() string {
	if d == nil {
		return ""
	}
	return d.path
}

// Load 读出整份映射。文件不存在 → 空映射（不是错误：还没有任何密钥的机器是正常状态）。
//
// 三条硬判据：
//   - **权限过宽即失败**（`mode & 0o077 != 0`）：同机其他用户可读的密钥文件不可接受，与 dsh 拒绝
//     加载可被属主之外读取的凭据文档同口径。这里给出可执行的出路（`chmod 600 <path>`）。
//   - 非法 JSON / 非对象 / 值不是字符串 / 键不是合法引用 → 失败。**不猜**：猜出来的"没有密钥"
//     会让人去查底座，而问题在这份文件里。
//   - 空文件 → 空映射（"还没写过"与"写成空的"不必区分）。
func (d *Doc) Load() (map[string]string, error) {
	if d == nil || d.path == "" {
		return nil, errors.New("credentials: 托管文档未配置")
	}
	fi, err := os.Stat(d.path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s 是目录，不是凭据文档", d.path)
	}
	if perm := fi.Mode().Perm(); perm&0o077 != 0 {
		return nil, fmt.Errorf("权限过宽（%04o）：密钥文档必须是 0600（现在同机其他用户可读）；先 `chmod 600 %s`", perm, d.path)
	}
	b, err := os.ReadFile(d.path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(b)) == "" {
		return map[string]string{}, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("不是合法 JSON（本文件是**严格 `引用 → 值` 映射**，刻意不是 dotenv）：%w", err)
	}
	if raw == nil {
		return nil, errors.New("顶层必须是对象（`{\"<引用>\": \"<值>\"}`），不是 null/数组/标量")
	}
	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if !validRef(k) {
			return nil, fmt.Errorf("键 %q 不是合法引用（非空、≤128 字节的可见 ASCII，不含空白）", k)
		}
		s, ok := v.(string)
		if !ok {
			return nil, fmt.Errorf("键 %q 的值不是字符串（严格映射：值只能是字符串）", k)
		}
		out[k] = s
	}
	return out, nil
}

// Save 原子写整份映射：**0600 + 同目录临时文件 + rename**。
//
// 已存在的文件权限走宽时，这里会**顺带治好**（重写成 0600）——读的那一侧仍然 fail-loud，于是
// "曾经暴露过"这件事不会因为一次写入而被静默抹掉，而"以后别再暴露"由写入保证。
func (d *Doc) Save(values map[string]string) error {
	if d == nil || d.path == "" {
		return errors.New("credentials: 托管文档未配置")
	}
	for k := range values {
		if !validRef(k) {
			return fmt.Errorf("键 %q 不是合法引用（非空、≤128 字节的可见 ASCII，不含空白）", k)
		}
	}
	if values == nil {
		values = map[string]string{}
	}
	// `encoding/json` 对 map 的键**排序输出**，所以同一份映射每次写出的字节相同（可 diff、可判等）。
	b, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')

	dir := filepath.Dir(d.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".credentials-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	// 任何失败分支都不留半截文件：临时文件先删再返回。
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}
	if _, err := tmp.Write(b); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, d.path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// Set 写入一个引用（读-改-写）。读失败即失败：**不**用"读不到就当空的"去覆盖一份损坏的文件——
// 那会把用户其余的密钥一起抹掉。
func (d *Doc) Set(ref, value string) error {
	if !validRef(ref) {
		return fmt.Errorf("引用 %q 非法", ref)
	}
	values, err := d.Load()
	if err != nil {
		return err
	}
	values[ref] = value
	return d.Save(values)
}

// Delete 删除一个引用（读-改-写；不存在时是无操作）。
func (d *Doc) Delete(ref string) error {
	values, err := d.Load()
	if err != nil {
		return err
	}
	if _, ok := values[ref]; !ok {
		return nil
	}
	delete(values, ref)
	return d.Save(values)
}

// validRef 校验引用串：非空、≤128 字节、全部是可见 ASCII 且不含空白。
//
// 刻意**不用**窄正则：引用是**我们自己的**命名空间（`providers/<id>` 与 `store:<ns>/<name>`），
// 而窄正则一旦漏掉某个合法形态，表现是"密钥写不进去"这种硬失败（比"写进了坏键"更难查）。
// 反过来，可见 ASCII + 无空白足以挡住真正的坏形状（控制字符、换行、超长）。
func validRef(ref string) bool {
	if ref == "" || len(ref) > 128 {
		return false
	}
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		if c <= ' ' || c > '~' {
			return false
		}
	}
	return true
}
