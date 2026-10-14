package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/morehao/ark-mate/internal/store"
	"github.com/morehao/ark-mate/pkg/capability"
	"github.com/morehao/ark-mate/pkg/credentials"
	"github.com/morehao/ark-mate/pkg/resource"
	"github.com/morehao/golib/glog"
)

// ——— 凭据面（v20：库是权威，`<MATE_HOME>/credentials.json` 是 0600 的可重建镜像）———
//
// 这个文件是"密钥从哪来"在 manager 侧的**唯一**落点，三件事各只有一处实现：
//
//	① **解析**：provider 拨号密钥走 `credentials.Layered` 的四层回落
//	   （继承环境 > 托管 > <cwd>/.env > <home>/.env），顺序实现只有 `pkg/credentials` 那一份；
//	   MCP 的 `store:` 引用没有环境变量锚点，只查"托管"这一层。
//	② **镜像**：库写完（成功）之后双写镜像。**镜像写失败绝不让整笔写失败、也不补偿**——
//	   镜像是库的可重建副本，为它回滚一次已提交的库写入是本末倒置；失败只 WARN + 由
//	   `mate doctor` 的镜像对账段列出来。
//	③ **对账**：装配期把库**并进**镜像（非破坏性：不删镜像里已有的键）。

// CredentialsFileName 托管镜像的文件名（`<MATE_HOME>/<它>`）。
const CredentialsFileName = "credentials.json"

// CredentialsPath 托管镜像的路径（home 为空 → 空串 = 未配置）。
func CredentialsPath(homeDir string) string {
	h := strings.TrimSpace(homeDir)
	if h == "" {
		return ""
	}
	return filepath.Join(h, CredentialsFileName)
}

// OpenCredentialSource 按约定装配四层凭据面：`<homeDir>/credentials.json` +
// `<cwdDir>/.env` + `<homeDir>/.env`。返回的第二个值是镜像路径（诊断面要打印它）。
//
// 打开镜像**不读内容**（`credentials.Open` 只登记路径）：读动作发生在每一次解析上，而权限
// 过宽时那次读会硬失败——"权限不合 fail-loud"因此落在真正读它的地方，而不是伪装成装配失败。
func OpenCredentialSource(homeDir, cwdDir string) (*credentials.Layered, string, error) {
	path := CredentialsPath(homeDir)
	if path == "" {
		return nil, "", errors.New("数据目录为空（MATE_HOME 未解析出来）：托管凭据文档没有落点")
	}
	doc, err := credentials.Open(path)
	if err != nil {
		return nil, "", err
	}
	return credentials.New(doc, cwdDir, homeDir), path, nil
}

// SetCredentials 登记四层凭据面。**装配期写一次、之后只读**（同 `EnableStore` 的口径）：
// 解析路径在请求里会被反复走到，再给它配一把锁只会让持 `s.mu` 的调用点（读模型）死锁。
func (s *Service) SetCredentials(l *credentials.Layered) { s.creds = l }

// SetCredentialCwd 登记**凭据 cwd**（`<cwd>/.env` 那一层的 cwd = 用户启动 `mate serve` 的目录）。
//
// 装配期写一次、之后只读（理由同 `SetCredentials`）。必须**显式登记**而不是用到时 `os.Getwd()`：
// 这个值要随网关条目交给网关**子进程**（见 `mcp.GatewayCredCwdEnv`），而网关的 cwd 是底座给的
// （会话工作区）——各自取自己的 cwd，同一个引用就会在两个进程里解析出不同的值，且完全静默。
func (s *Service) SetCredentialCwd(dir string) { s.credCwd = strings.TrimSpace(dir) }

// credentialCwd 登记过的凭据 cwd（空 = 装配期没登记）。
func (s *Service) credentialCwd() string { return s.credCwd }

// credentialDoc 托管镜像句柄（未装配 → nil）。
func (s *Service) credentialDoc() *credentials.Doc {
	if s.creds == nil {
		return nil
	}
	return s.creds.Managed
}

// providerEnvKeyNames 该供应商在**已注册 runtime**上的候选环境变量名（四层回落的"继承环境"
// 与两份 dotenv 都按它们取值）。
//
// 为什么用这个名字而不是底座自家的什么 `DEEPSEEK_API_KEY`：平台注入子进程 env 用的就是它
// （`resource.RefFor(spec.Credential.RefPattern, id)`，逐供应商唯一，ADR-16），而参照实现 dsh
// 自己的查找优先级就是"继承环境 > 存储文档"、且存储文档的 refs **按环境变量名**存放。
// 于是这里查的键与底座真正会读的键是**同一个**——分层顺序对齐不是巧合，是同一份命名。
func (s *Service) providerEnvKeyNames(providerID string) []string {
	if s.reg == nil || strings.TrimSpace(providerID) == "" {
		return nil
	}
	specs := s.reg.List()
	seen := make(map[string]bool, len(specs))
	out := make([]string, 0, len(specs))
	for _, spec := range specs {
		name := resource.RefFor(spec.Credential.RefPattern, providerID)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// providerKey 一次 provider 拨号密钥的解析结果。
//
// 它就是 `credentials.Resolution` 再加一件 manager 才知道的事：命中"继承环境"那一层时，
// **是哪个环境变量命中的**（层序里这一层会遮住面板里保存的那条，读面必须说得出来）。
type providerKey struct {
	Value    string
	Source   credentials.Source
	EnvKey   string
	Problems []string
}

// resolveProviderKey 解析一条 provider 的拨号密钥（provider 面**唯一**入口）。
//
// 四层顺序由 `pkg/credentials.Layered` 一处实现；这里只做一件它做不到的事——**把"库"并进
// 托管那一层**，因为库才是权威、镜像只是副本：
//
//   - 分层命中 `managed`（镜像里有）时，与库对一次账：**库里有值才采用**（两边都有时以库为准）。
//     镜像里有、库里没有 = 镜像陈旧或曾写入失败——**不采用**，否则"在面板上清掉的密钥"会被
//     一份陈旧镜像悄悄撤销（那是把一次删除反转成静默的 no-op）。这条只在**否决**一个命中，
//     不改变层与层的先后，所以顺序实现仍然只有一处。
//   - 分层什么都没命中、也没有"读不了"的问题时，库里那一份**仍然可用**（镜像丢了不丢密钥）。
//   - 分层报"某层读不了"（镜像权限过宽 / 形状坏掉 / `.env` 写坏了）时，问题原样带出去；
//     只要库里那一份在，密钥就还是可用的——权威在库，"镜像坏了"不等于"密钥没了"。
//
// 没有库（memory 模式）时托管层就是镜像文档本身：文件即事实源。
func (s *Service) resolveProviderKey(ctx context.Context, d *providerDef) providerKey {
	if d == nil {
		return providerKey{}
	}
	envKeys := s.providerEnvKeyNames(d.ID)
	req := credentials.Request{Ref: d.KeyRef, EnvKeys: envKeys}
	var res credentials.Resolution
	if s.creds == nil {
		res = credentials.Resolution{Problems: []string{"凭据分层面未装配：只有库里的那一份可用"}}
	} else {
		res = s.creds.Resolve(req)
	}
	out := providerKey{Value: res.Value, Source: res.Source, Problems: res.Problems}
	if out.Source == credentials.SourceEnv {
		out.EnvKey = s.matchEnvKey(envKeys, out.Value)
	}
	if s.st == nil || strings.TrimSpace(d.ID) == "" {
		return out
	}
	dbKey, found, err := s.st.ProviderKey(ctx, d.ID)
	if err != nil {
		out.Problems = append(out.Problems, "读库里的密钥失败："+err.Error())
		return out
	}
	if !found {
		// 行不在了（装载与写面之间的窗口）：不采用镜像里那一份。
		if out.Value != "" {
			return providerKey{Problems: []string{
				"库里没有这条供应商了（可能刚被删掉）：不采用托管文档里那一份"}}
		}
		return out
	}
	switch {
	case out.Source == credentials.SourceManaged && strings.TrimSpace(dbKey) == "":
		return providerKey{Problems: []string{
			"托管文档里有这条密钥而库里没有（镜像陈旧，或写入/删除曾失败）：**不采用**镜像里那一份" +
				"（否则在面板上清掉的密钥会被它悄悄撤销）；在「设置 · 模型」里重设一次即可对齐"}}
	case out.Source == credentials.SourceManaged:
		out.Value = dbKey // 两边都有时以库为准。
		return out
	case out.Value == "" && strings.TrimSpace(dbKey) != "":
		return providerKey{Value: dbKey, Source: credentials.SourceManaged, Problems: out.Problems}
	}
	return out
}

// oneKeyInParens 把一个可能为空的名字渲染成括号后缀（空 → 空串，不留一对空括号）。
func oneKeyInParens(name string) string {
	if strings.TrimSpace(name) == "" {
		return ""
	}
	return "（" + name + "）"
}

// matchEnvKey 找出**哪一个**候选环境变量给出了这个值（用与分层同一把取值函数，避免两处口径分叉）。
func (s *Service) matchEnvKey(keys []string, value string) string {
	if value == "" {
		return ""
	}
	env := os.Getenv
	if s.creds != nil && s.creds.Env != nil {
		env = s.creds.Env
	}
	for _, k := range keys {
		if v := env(k); strings.TrimSpace(v) != "" && v == value {
			return k
		}
	}
	return ""
}

// mirrorValue 读托管镜像里的一条引用（未装配/读不了 → 空）。
func (s *Service) mirrorValue(ref string) (string, bool) {
	doc := s.credentialDoc()
	if doc == nil || strings.TrimSpace(ref) == "" {
		return "", false
	}
	values, err := doc.Load()
	if err != nil {
		return "", false
	}
	v, ok := values[ref]
	return v, ok
}

// mirrorSet / mirrorDelete 库写完之后的**双写**（失败只 WARN，由调用方决定要不要进 problems）。
func (s *Service) mirrorSet(ref, value string) error {
	doc := s.credentialDoc()
	if doc == nil {
		return nil
	}
	if strings.TrimSpace(value) == "" {
		return s.mirrorDelete(ref)
	}
	if err := doc.Set(ref, value); err != nil {
		glog.Warnw(context.Background(),
			"credentials: 托管镜像写入失败（库已写成功；镜像可由库重建，不阻断也不补偿）",
			"ref", ref, "err", err)
		return err
	}
	return nil
}

func (s *Service) mirrorDelete(ref string) error {
	doc := s.credentialDoc()
	if doc == nil {
		return nil
	}
	if err := doc.Delete(ref); err != nil {
		glog.Warnw(context.Background(),
			"credentials: 托管镜像删除失败（库已改成功；镜像可由库重建，不阻断也不补偿）",
			"ref", ref, "err", err)
		return err
	}
	return nil
}

// SyncCredentialMirror 把库里的凭据**并进**托管镜像，返回改动的条目数（幂等）。
//
// **非破坏性**：只覆盖/补齐库里有值的那几条，**不删**镜像里已有的其它键。理由有两条：
//
//   - "镜像有而库里没有"是 `mate doctor` 要**报出来**的一种事实（用户手写过、或某次镜像
//     删除失败过），装配期顺手删掉它，那条判据就永远看不到东西了；
//   - 镜像里有用户手写的键，抹掉它就是替用户删数据（与"搬运不删旧密文"同一条纪律）。
//
// 库里没有的键**绝不回灌**（设计 §3）：镜像不是事实源，回灌只会把一份陈旧文件变成权威。
func (s *Service) SyncCredentialMirror(ctx context.Context, reason string) (int, error) {
	if s.st == nil {
		return 0, nil // memory 模式：镜像就是事实源，绝不用库（没有库）去覆盖它。
	}
	doc := s.credentialDoc()
	if doc == nil {
		return 0, nil
	}
	return syncCredentialMirror(ctx, s.st, doc, reason)
}

// SyncCredentialMirrorFromStore 包级版本：给**不走 Service** 的调用方用（`mate provider import`
// 与 `mate capability import` 这两条 CLI 路径各自开库，没有装配出来的 Service）。
//
// homeDir 是镜像的锚点（`<MATE_HOME>/credentials.json`），**不是** ProviderDir 的父目录——
// v20 起凭据与档案目录无关，只有旧机制的遗留物还按后者取（见 provider_doctor.go）。
func SyncCredentialMirrorFromStore(ctx context.Context, st *store.Store, homeDir, reason string) (int, error) {
	if st == nil || strings.TrimSpace(homeDir) == "" {
		return 0, nil
	}
	doc, err := credentials.Open(CredentialsPath(homeDir))
	if err != nil {
		return 0, err
	}
	return syncCredentialMirror(ctx, st, doc, reason)
}

// syncCredentialMirror 合并实现（两个入口共用：一处实现，两条调用路径不可能分叉）。
func syncCredentialMirror(ctx context.Context, st *store.Store, doc *credentials.Doc, reason string) (int, error) {
	values, err := doc.Load()
	if err != nil {
		return 0, err
	}
	changed := 0
	put := func(ref, value string) {
		if strings.TrimSpace(ref) == "" || strings.TrimSpace(value) == "" {
			return
		}
		if cur, ok := values[ref]; ok && cur == value {
			return
		}
		values[ref] = value
		changed++
	}
	rows, err := st.ListProviderDefinitions(ctx)
	if err != nil {
		return changed, err
	}
	for i := range rows {
		if strings.TrimSpace(rows[i].Key) == "" {
			continue // 空串 = 没有密钥，镜像里不该有这一条（存量条目由 doctor 报出，不在这里删）
		}
		put(providerRef(rows[i].ID), rows[i].Key)
	}
	secrets, err := st.ListMcpSecrets(ctx)
	if err != nil {
		return changed, err
	}
	for i := range secrets {
		put(secrets[i].Ref, secrets[i].Value)
	}
	if changed == 0 {
		return 0, nil
	}
	if err := doc.Save(values); err != nil {
		return 0, err
	}
	glog.Infow(ctx, "credentials: 托管镜像已按库对账（库是权威，镜像可重建）",
		"changed", changed, "total", len(values), "reason", reason)
	return changed, nil
}

// resolveStoreSecret 解析一条 `store:<ns>/<name>` 引用（MCP 密钥面）。
//
// 与 provider 密钥不同，它**没有环境变量锚点**（引用是定义体里写死的），所以没有四层回落：
// 有库时查库（权威），没有库时查镜像文档（memory 模式文件即事实源）。库里没有而镜像里有
// 时**不采用**镜像那一份（理由同 provider：清了密钥不能被陈旧镜像撤销），只把这件事说清楚。
func (s *Service) resolveStoreSecret(ctx context.Context, ref string) (string, error) {
	if s.st != nil {
		v, ok, err := s.st.McpSecret(ctx, ref)
		if err != nil {
			return "", fmt.Errorf("读库里的密钥失败：%w", err)
		}
		if ok && strings.TrimSpace(v) != "" {
			return v, nil
		}
		if _, mirrored := s.mirrorValue(ref); mirrored {
			return "", fmt.Errorf("托管文档里有 %s 而库里没有（镜像陈旧，或写入/删除曾失败）：**不采用**"+
				"镜像里那一份；在面板里重新填写该 MCP 的密钥即可对齐", ref)
		}
		// 出路**只写用户今天真能做到的那一条**：库里 `mcp_secrets` 的值这一批**还没有写入口**
		// （REST/CLI/面板都没有），所以"把值写进库"对用户不是一条出路——写它等于让人去找一个
		// 不存在的入口。能改的是定义里的**引用**，而 `env:` 那一层由进程环境/`.env` 提供。
		return "", fmt.Errorf("库里没有这条密钥（引用 %s）：在能力面板里把该 MCP 的引用改成 "+
			"env:<ENV_NAME>（值由进程环境或 .env 提供）", ref)
	}
	v, ok := s.mirrorValue(ref)
	if !ok || strings.TrimSpace(v) == "" {
		return "", fmt.Errorf("托管文档里没有这条密钥（引用 %s）：memory 模式下它就在 "+
			"`<MATE_HOME>/%s` 里，或把定义里的引用改成 env:<ENV_NAME>", ref, CredentialsFileName)
	}
	return v, nil
}

// translateLegacyMcpRefs 把一条 MCP 定义文档里的旧前缀引用**就地**翻成 `store:`，
// 返回被翻过的条数（0 = 没有旧前缀，调用方不必重新序列化）。
//
// 它只改**内存里的文档**，不写回用户文件（那条纪律是"导入不删不回写"）；调用方重新
// `Marshal()` 之后，写进库的正文才是翻译后的形态。翻译点本身只有 `pkg/capability` 那一处。
func translateLegacyMcpRefs(ctx context.Context, doc *capability.DefinitionFile, where string) int {
	n := 0
	for _, key := range []string{"secret_env", "secret_headers"} {
		var bindings map[string]string
		ok, err := doc.Decode(key, &bindings)
		if !ok || err != nil || len(bindings) == 0 {
			continue
		}
		changed := false
		for k, ref := range bindings {
			if translated, legacy := capability.TranslateLegacySecretRef(ref); legacy {
				warnLegacySecretRef(ctx, ref, where+" 的 "+key)
				bindings[k] = translated
				changed = true
				n++
			}
		}
		if changed {
			_ = doc.Set(key, bindings)
		}
	}
	return n
}
