package session

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/morehao/ark-mate/pkg/acl"
	"github.com/morehao/ark-mate/pkg/object/objid"
	"github.com/morehao/ark-mate/pkg/runtime"
	"github.com/morehao/golib/glog"
)

// ——— 会话的**启动 / 恢复 / 换句柄**（P4a 正文第五十六刀：从 `internal/manager` 搬入）———
//
// 这一族回答的是「一个平台会话怎么挂到一条 ACP 会话上」：
//
//	StartSession —— 新建（session/new）
//	Ensure   —— 不在活跃集合时按需重建（原生 resume 优先、handoff memo 兜底）
//	ConnectSession —— 在指定代际上重开会话（重建与代际迁移共用同一条路径）
//	Attach   —— 把句柄装进活跃集合（替换同 id 的旧句柄）
//	ApplyModel —— 会话级模型的应用（建会话与「会话内改模型」共用）
//
// 它们只依赖三类事实：会话域自己的记录/句柄表、`pkg/runtime` 的注册表与投影、以及装配侧交来的
// 窄端口（选定的底座 / 拨号 / 模型解析 / MCP 注入 / 目录缓存 / 诊断 / 运行期状态）。
// **连接池与权限/提问的裁决不在这里**——前者属执行面，后者属交互面。

// DefaultHandoffMaxChars bounded handoff memo 默认字符预算（§18 D-3；env MATE_HANDOFF_MAX_CHARS 覆盖）。
const DefaultHandoffMaxChars = 6000

// StartOptions 一次 StartSession 的入参。
//
// Cwd 为空时回落当前工作目录，**仅为 demo/RunOnce 路径保留**：会话创建路径
// （Service.CreateSession）必须先经 ResolveWorkspace 定出工作空间再传进来，
// 绝不能让「平台站在哪」成为用户会话的沙箱边界。
type StartOptions struct {
	// SessionID 平台会话 uuid；空 = 由本层生成。
	// 该 id 在 session/new **之前**定号，代建工作空间的目录名以它为准。
	SessionID string
	// Cwd 会话工作空间（规范化后的绝对路径）；空 = 用当前工作目录（demo 语义）。
	Cwd string
	// WorkspaceTemporary 该 cwd 是否由**平台**给出（= 默认工作空间；随响应透出，
	// 前端不得从路径形状反推）。
	WorkspaceTemporary bool
	// WorkspaceID 归属的工作空间登记 id（空 = 未登记 → 读面归默认工作空间）。
	//
	// 与 Cwd 的分工见 SessionRec.WorkspaceID：Cwd 是执行事实（ACP cwd、沙箱边界），
	// WorkspaceID 是归属事实（左栏分组）。两个字段一起传，是因为**定稿发生在同一处**。
	WorkspaceID string
	// Model 会话级模型。**`Model` 字段装的是平台模型行 id**（`mdl_…`；空 = 用平台默认那一行），
	// 由 `ResolveModel` 端口解析成"行 + 运行时值"——库里/wire 上流通的是**身份**，
	// 底座认的不透明值由 adapter 现算。
	Model runtime.ModelConfig
	// Origin 会话来源（平台判定，非用户输入）：'' = 人工，'automation:<id>' = 该 automation 触发。
	// 只用它给会话列表打标；不参与任何鉴权判断。
	Origin string
	// Selection 该会话声明的能力选定（M3；nil = 未声明 → 全量按 runtime 过滤）。
	//
	// **这里刻意没有独立的 `Fingerprint` 字段**：指纹在 StartSession 里由本字段现算
	// （`Plane.CapabilityFingerprint`）。两者分开传的后果是"指纹按 A 算、注入按 B 做"，
	// 而那种分裂不会报错——只会让会话拿到与代际台账不符的能力集，且只在重启后显现。
	Selection *CapabilitySelection
	// NativeMode 会话**钉住**的原生权限模式（v29；'' = 不注入）。
	//
	// 它同时是**暖进程键的一维**：模式只能以进程 env 下发，两个模式不同的会话不能共用一个
	// 暖进程。合并链在用例层建会话处一次性解析，这里只负责把它带下去。
	NativeMode string
}

// CreateResult 是 Start 的结果。
type CreateResult struct {
	SessionID string
	Runtime   string
	Status    string
	EventSeq  int64 // session.created 事件的 bus 水位（供 WS attach 续传）
}

// ConfigCatalog 模型目录缓存的写口（建会话/恢复/改模型时刷新，失败取值标灰）。
type ConfigCatalog interface {
	ConfigOptionsSink
	MarkRejected(runtimeID, value, reason string)
	ClearRejection(runtimeID, value string)
}

// LifecycleDeps 造一个 Lifecycle 所需的全部事实与端口。
type LifecycleDeps struct {
	Plane    *Plane
	Handles  *Handles
	Registry *runtime.Registry
	// SelectedSpec 构建期选定的那个底座（`runtime_not_found` 的唯一铸点在执行面）。
	SelectedSpec func() (runtime.Spec, error)
	// Acquire 取（或拨）一条连接，返回它的 ACP 客户端。
	Acquire func(ctx context.Context, spec runtime.Spec, fingerprint, nativeMode string) (*acl.Client, error)
	// ResolveModel 平台模型行 id → (行 id, 运行时值)。
	ResolveModel func(ctx context.Context, runtimeID, modelID string) (string, runtime.ModelConfig, error)
	// ProviderKeyReady 该供应商当前是否有可用密钥（false + 原因）。nil = 不做这项前置。
	//
	// 只对声明了 ModelCatalogNeedsCredential 的 runtime 生效（见 applyModel）。
	ProviderKeyReady func(ctx context.Context, providerID string) (bool, string)
	// McpServers 该会话该下发的 MCP 条目（注册表 + 传输能力 + 选定过滤后的结果）。
	McpServers func(runtimeID string, sel *CapabilitySelection) []acl.McpServer
	// Catalog 模型目录缓存。
	Catalog ConfigCatalog
	// Diagnostics 运行期诊断（起不来 / 恢复失败 / 模型被拒的留痕）。
	Diagnostics DiagnosticsSink
	// SetRuntimeState 运行期状态回落（建连失败时置 stopped）。
	SetRuntimeState func(state string)
	// ApplyReason 把运行时拒绝模型的错误翻成给用户看的「不可用原因」。
	ApplyReason func(err error) string
}

// Lifecycle 会话的启动/恢复/换句柄。
type Lifecycle struct {
	plane    *Plane
	handles  *Handles
	registry *runtime.Registry

	selectedSpec     func() (runtime.Spec, error)
	acquire          func(ctx context.Context, spec runtime.Spec, fingerprint, nativeMode string) (*acl.Client, error)
	resolveModel     func(ctx context.Context, runtimeID, modelID string) (string, runtime.ModelConfig, error)
	providerKeyReady func(ctx context.Context, providerID string) (bool, string)
	mcpServers       func(runtimeID string, sel *CapabilitySelection) []acl.McpServer
	catalog          ConfigCatalog
	diagnostics      DiagnosticsSink
	setState         func(state string)
	applyReason      func(err error) string

	handoffMaxChars int
}

// NewLifecycle 造一个生命周期实现。所有端口在装配期一次接上。
func NewLifecycle(d LifecycleDeps) *Lifecycle {
	return &Lifecycle{
		plane: d.Plane, handles: d.Handles, registry: d.Registry,
		selectedSpec: d.SelectedSpec, acquire: d.Acquire, resolveModel: d.ResolveModel,
		providerKeyReady: d.ProviderKeyReady,
		mcpServers:       d.McpServers, catalog: d.Catalog, diagnostics: d.Diagnostics,
		setState: d.SetRuntimeState, applyReason: d.ApplyReason,
		handoffMaxChars: DefaultHandoffMaxChars,
	}
}

// SetHandoffMaxChars 设置 bounded handoff memo 字符预算（0 = 关闭 memo 兜底）。
func (l *Lifecycle) SetHandoffMaxChars(n int) { l.handoffMaxChars = n }

// StartSession 在**构建期选定的** runtime 上建立持久 ACP 会话（session/new，绝对 cwd），
// 并发布 session.created。不发起 prompt；轮次由 Runner 负责。
func (l *Lifecycle) StartSession(ctx context.Context, opt StartOptions) (*CreateResult, error) {
	// **单点铸码**：`runtime_not_found` 只由执行面铸（`SelectedSpec`）。
	spec, err := l.selectedSpec()
	if err != nil {
		return nil, err
	}
	// 能力选定校验（M3-3）：**在建连之前**。位次很关键——它既要在已知 runtime 之后
	// （可达性是 runtime 相关的），又要在任何副作用之前（不建会话/不拉进程/不写库）。
	// 不可达就报错而非静默丢弃：静默丢弃等于让用户以为"选了就生效了"。
	if err := l.plane.ValidateSelection(opt.Selection, spec.ID); err != nil {
		return nil, err
	}
	// 模型解析（v26 定案「辰」）：请求里给的是**平台模型行 id**（空 = 用平台默认那一行），
	// 这里解析成**行**并现算底座认的不透明值。位次刻意与能力校验并列——都要在**任何副作用之前**
	// （不建会话/不拉进程/不写库）：一个查不到的模型 id 不该先把底座拉起来再失败。
	platformModelID, modelCfg, err := l.resolveModel(ctx, spec.ID, opt.Model.Model)
	if err != nil {
		return nil, err
	}
	// 能力指纹由**选定**现算（M3）：缺省路径 opt.Selection == nil → 空指纹 →
	// 代际键与引入代际之前逐字相同（R11）。指纹与下面注入用的 opt.Selection 同源，
	// 不存在"按 A 算指纹、按 B 注入"的可能。
	fingerprint := l.plane.CapabilityFingerprint(spec.ID, opt.Selection)
	client, err := l.acquire(ctx, spec, fingerprint, opt.NativeMode)
	if err != nil {
		if l.setState != nil {
			l.setState(AgentStateStopped)
		}
		// A3：底座起不来 / 握手失败时记诊断（含该阶段的 stderr 尾巴），供
		// `GET /runtime/diagnostics` 外露。今天唯一的启动路径就是"建会话把它拉起来"，所以落在这里。
		if l.diagnostics != nil {
			if ae := acl.Classify(err); ae != nil {
				l.diagnostics.RecordRuntimeError(opt.SessionID, ae, ae.StderrTail)
			}
		}
		return nil, err
	}
	cwd := opt.Cwd
	if cwd == "" {
		// demo/RunOnce 语义（会话创建路径已在用例层定出工作空间，不会走到这里）。
		cwd, _ = os.Getwd()
	}
	// 平台会话 id 在 session/new **之前**定号：代建工作空间的目录名以它为准
	// （目录先于 session/new 存在，底座对 cwd 做存在性校验）。
	convID := opt.SessionID
	if convID == "" {
		convID = objid.New(objid.Session)
	}
	// P2 MCP 注入：平台注册表 MCP 子集按「会话选定 > 全量」+ runtime 传输能力过滤后
	// 随 session/new 下发。**必须传 opt.Selection**：指纹就是按它算出来的，注入若用别的口径，
	// 会话拿到的东西会与代际台账不符。
	sess, err := client.NewSession(ctx, acl.SessionOptions{
		Cwd:        cwd,
		McpServers: l.mcpServers(spec.ID, opt.Selection),
	})
	if err != nil {
		return nil, err
	}

	// P3 双 id（D-1，用户已授权破坏性变更）：会话对外 id = 平台铸造的 `ses_<ULID>`（隐藏底座 id
	// 形状、跨重启稳定定位）；原生 provider id 存 sess/RuntimeSessionID 列，供恢复承载（load/resume）。
	as := NewActiveSession(HandleParams{
		ID: convID, Runtime: spec.ID, Fingerprint: fingerprint, NativeMode: opt.NativeMode,
		ProviderID: sess.ID(), Client: client, Session: sess,
		Projection: runtime.NewProjection(spec, sess),
	})
	// 会话级模型应用（运行时派生模型面的落地）：
	//   - 值由 `modelCfg` 给出（平台行 → adapter 现算）；
	//   - 运行时**拒绝**该值 → 建会话失败（错误上抛给调用方，绝不静默回落到底座默认模型）；
	//   - unsupported_*（运行时未广告模型配置项）→ 忽略（无模型语义）；
	//   - 成功 → 用返回的**全量**配置项刷新平台目录缓存（顺带清掉曾失败值的标记）。
	if err := l.applyModel(ctx, as, spec, modelCfg); err != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	// W1：底座原文 → 平台侧的值（`rt_` 壳）。**失败即返回错误、不建 rec**——把裸 id 落库会让
	// "平台 id 与底座 id 形状可分"这条不变式当场失效，而那是静默的（守卫② 事后才发现）。
	// `HandleParams.ProviderID` 刻意保留**原文**：它是句柄表原生索引的键，与 ACP 回调同源。
	providerSessID, err := objid.WrapRuntime(sess.ID())
	if err != nil {
		_ = sess.Close(context.WithoutCancel(ctx))
		return nil, err
	}
	l.handles.Put(as)

	l.plane.RegisterSession(&SessionRec{ID: as.ID, Cwd: cwd,
		WorkspaceID: opt.WorkspaceID, WorkspaceTemporary: opt.WorkspaceTemporary,
		// **落库的是身份**（平台模型行 id），不是运行时值——定案「辰」的核心。
		ModelID: platformModelID, RuntimeID: spec.ID, Fingerprint: fingerprint, Origin: opt.Origin,
		RuntimeSessionID: providerSessID, Status: SessionStatusInProgress,
		// 钉住的选定**与指纹一起**落库（M3-3 起）：两者必须同源，缺一则恢复路径
		// 要么注入集不对、要么代际挂错。
		CapabilityJSON: MarshalCapabilitySelection(opt.Selection),
		// 钉住的原生权限模式（v29）：它决定这个会话跑在**哪个暖进程**上，所以必须与其它
		// 钉住字段一起落库——漏了它，重启后该会话会悄悄换到一个"不注入原生权限"的进程上。
		NativeMode: opt.NativeMode})
	l.plane.BeginTranscript(as.ID, spec.ID, cwd, opt.WorkspaceTemporary) // 未启用时 no-op
	evt := l.plane.Publish(BusSessionCreated, as.ID, map[string]any{
		"session_id": as.ID, "runtime": spec.ID,
	})
	return &CreateResult{SessionID: as.ID, Runtime: spec.ID, Status: SessionStatusInProgress, EventSeq: evt.Seq}, nil
}

// Ensure 返回活跃会话；不在活跃集合但平台有记录时按需重建。
// 恢复次序（P3 F2/F3，§5.6）：
//  1. 原生 resume（首选）：acquire → 按该底座声明的能力走 LoadSession 或 ResumeSession
//     恢复同一 provider session → reason=restart。
//  2. handoff memo 兜底（原生不可用时）：底座未声明 load/resume 能力、无 provider session
//     （旧数据）、会话记录的 runtime ≠ 当前构建期固定的那个（跨 runtime 续聊）、或原生恢复失败 →
//     在该 runtime 建 fresh session + 把历史压成 bounded memo 挂到首条 prompt（reason=handoff），
//     并更新 rec 的 runtime_session_id / runtime_id（记忆与 sqlite 同步）。
//
// **可续聊性只看会话是否还存在**（ADR-4：执行可丢弃、会话平台拥有）：上一轮的终态是轮次结果的事实，
// 不是「会话作废」的判据——旧语义把终态会话按未找到处理，会让 daemon 每重启一次就作废全部历史会话。
// 只有真正不可用的情形才报错：会话不存在（session_not_found）、runtime 未注册（runtime_not_found）、
// 原生与 memo 都不可用（resume_unavailable）。
//
// **重开即迁移（M3-6）**：重建时按**当前定义**重算代际指纹，于是"改了 skill 正文/MCP 定义后
// 会话在进程死掉重开"这一路径天然落在最新代际上；算不出或按新代际重开失败 → 回退钉住的旧代际
// 继续跑（可用性优先，见代际迁移的四条纪律）。
func (l *Lifecycle) Ensure(ctx context.Context, sessionID string) (*ActiveSession, error) {
	if as := l.handles.Get(sessionID); as != nil {
		return as, nil
	}
	rec := l.plane.Rec(sessionID)
	if rec == nil {
		return nil, acl.NewError(acl.ClassRequest, "session_not_found",
			fmt.Sprintf("未知会话 %q", sessionID), nil)
	}
	// runtime 选择：**构建期选定的那一个**。
	// 若会话记录里的原生 runtime 与当前不同（改过构建 / 老数据），下面 Connect 的 `sameRuntime`
	// 判据自然为 false，走 fresh + handoff memo 重灌，会话照样能续。
	spec, err := l.selectedSpec()
	if err != nil {
		return nil, err
	}
	cwd := rec.Cwd
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	// **恢复路径也要带上会话钉住的选定**：漏改这一处的后果是"重启后能力集悄悄换一套"。
	// 与指纹的分工：指纹决定挂到哪个代际（连接复用），选定决定下发哪些 MCP。
	sel := ParseCapabilitySelection(rec.CapabilityJSON)
	// 目标代际：有声明就**按当前定义现算**——重开即迁移，落下就是最新代际（M3-6）。
	// 缺省路径（sel == nil）现算结果恒为空 → 保持钉住的空指纹 → 与引入代际之前逐字一致（R11）。
	pinned := rec.Fingerprint
	want := l.plane.CapabilityFingerprint(spec.ID, sel)
	if want == "" {
		want = pinned
	}
	as, reason, err := l.ConnectSession(ctx, rec, spec, cwd, sel, want)
	if err != nil && want != pinned {
		// ④ 迁移失败 → 回退钉住的旧代际继续跑（可用性优先）。**不**把会话变成不可用：
		// "能力没换过来"是诊断层面的事实，不是这一轮发不出去的理由。
		failed := err
		as, reason, err = l.ConnectSession(ctx, rec, spec, cwd, sel, pinned)
		if err == nil {
			l.NoteGenerationFallback(sessionID, spec.ID, pinned, want, failed)
		}
	}
	if err != nil {
		return nil, err
	}
	// 事件语义：重开时**真的换了代际** → reason=generation（前端据此提示"能力已更新"，
	// 与"进程重启后原生续接"区分开）；否则沿用 restart/handoff。
	if want != pinned && as.Fingerprint == want {
		reason = "generation"
	}
	l.Attach(as, reason)
	return as, nil
}

// ConnectSession 在 `fingerprint` 指定的代际上重开一条会话（原生 resume 优先、handoff memo 兜底），
// 并把「实际挂上的代际」写回会话记录。**不碰活跃集合**（装配由 Attach 做）——
// 于是"进程死后重建"与"轮次边界迁移"两条路径共用同一段开会话逻辑，不会各自漂移。
//
// 记录用**副本**更新后整体写回（`RegisterSession` 会同时持久化）：中途失败时调用方手里的
// rec 保持原样，回退旧代际那一次才不会被半成品污染。
func (l *Lifecycle) ConnectSession(ctx context.Context, recIn *SessionRec,
	spec runtime.Spec, cwd string, sel *CapabilitySelection, fingerprint string) (*ActiveSession, string, error) {
	rec := *recIn
	// **恢复路径也要带上会话钉住的指纹与权限模式**：漏改这一处的后果分别是"重启后能力集悄悄换了
	// 一套"与"重启后沙箱边界悄悄变宽/变窄"——两者都不会报错。
	client, err := l.acquire(ctx, spec, fingerprint, rec.NativeMode)
	if err != nil {
		return nil, "", err
	}
	// **恢复路径必须用会话钉住的选定**（不是全量）：它从 `sessions.capability_json` 读回。
	// 漏了这一步的后果是会话重启后注入集悄悄换一套，而且没有任何报错。
	opts := acl.SessionOptions{Cwd: cwd, McpServers: l.mcpServers(spec.ID, sel)}
	caps := client.Capabilities()
	sameRuntime := spec.ID == rec.RuntimeID && rec.RuntimeID != ""
	// `nativeID` 是**平台侧的值**（`rt_` 壳）；给底座的必须是**原文**。
	//
	// U1：`rt_ses_…` 底座认不出来 → `LoadSession` 失败 → 打一条 WARN 后走 handoff memo 兜底
	// （ADR-4 明确允许的降级）——用户侧只看到"上下文丢了"，**不响**。所以这里严格解包，
	// 失败即返回错误，不降级（"让错的那次在边界上当场失败"是这两条函数存在的全部理由）。
	nativeID := rec.RuntimeSessionID
	rawNativeID := ""
	if nativeID != "" {
		raw, uerr := objid.UnwrapRuntime(nativeID)
		if uerr != nil {
			return nil, "", uerr
		}
		rawNativeID = raw
	}
	resumeOK := sameRuntime && rawNativeID != "" &&
		(caps.Raw.LoadSession || caps.Raw.SessionCapabilities.Resume != nil)

	var sess *acl.Session
	var memo string
	reason := "restart"
	switch {
	case resumeOK:
		if caps.Raw.LoadSession {
			sess, err = client.LoadSession(ctx, rawNativeID, opts)
		} else {
			sess, err = client.ResumeSession(ctx, rawNativeID, opts)
		}
		if err != nil {
			glog.Warnw(ctx, "原生恢复失败，转 handoff memo 兜底", "session", rec.ID,
				"runtime", spec.ID, "runtime_session", nativeID, "err", err)
			resumeOK = false
		}
	}
	if !resumeOK {
		if l.handoffMaxChars <= 0 {
			return nil, "", acl.NewError(acl.ClassRequest, "resume_unavailable",
				fmt.Sprintf("会话 %q 无法原生恢复（runtime %q 无 load/resume 或 provider 会话无效），且 memo 兜底关闭",
					rec.ID, spec.ID), nil)
		}
		sess, err = client.NewSession(ctx, opts)
		if err != nil {
			return nil, "", err
		}
		memo = l.plane.HandoffMemo(rec.ID, l.handoffMaxChars)
		reason = "handoff"
		// W2（最容易漏的一处：它在"原生恢复失败/迁移"分支里）：新会话的底座原文同样要加壳，
		// 否则换底座/换会话之后 id 会退化成裸值——库里的形状就变成了两种。
		wrappedNew, werr := objid.WrapRuntime(sess.ID())
		if werr != nil {
			return nil, "", werr
		}
		// 迁移登记：fresh session 顶替原 provider id；跨 runtime 时同步 runtime_id。
		if nativeID != wrappedNew || spec.ID != rec.RuntimeID {
			rec.RuntimeSessionID = wrappedNew
			rec.RuntimeID = spec.ID
		}
	}
	// 代际落账：**实际挂上的**指纹写回记录（迁移后这里就是新值；回退时是旧值）。
	if rec.Fingerprint != fingerprint {
		rec.Fingerprint = fingerprint
	}
	l.plane.RegisterSession(&rec)
	as := NewActiveSession(HandleParams{
		ID: rec.ID, Runtime: spec.ID, Fingerprint: fingerprint, NativeMode: rec.NativeMode,
		ProviderID: sess.ID(), Client: client, Session: sess,
		Projection: runtime.NewProjection(spec, sess), Memo: memo,
	})
	// 会话级模型在恢复后重新断言一次（平台记录是权威意图；原生恢复未必带回该状态）：
	// **best-effort** —— 恢复不是用户在选模型，运行时若已不再提供该值（换了 provider/档案），
	// 只留痕 + 标灰，不让老会话因为一个失效取值而打不开。
	//
	// `Projection() == nil` = 该 runtime 没有会话级投影（`NewProjection` 按 runtime id 派生；
	// 测试桩就是这一类），此时没有模型语义可言——**必须显式跳过**，否则对 nil 接口调方法直接 panic。
	if as.Projection() == nil {
		return as, reason, nil
	}
	if rec.ModelID != "" {
		// v26：`rec.ModelID` 是**平台行 id**（库里不再存底座词表的串），所以恢复时必须**重新现算**
		// 运行时值。算不出来（行被删了 / 存量回填还没跑到）→ **跳过重放**并留痕：恢复不是用户在
		// 选模型，不该让老会话因为一个失效引用而打不开（与上面 best-effort 的口径一致）。
		_, recCfg, rerr := l.resolveModel(ctx, spec.ID, rec.ModelID)
		if rerr != nil || recCfg.Model == "" {
			glog.Warnw(ctx, "恢复时按平台模型行重算运行时值失败（跳过重放，继续用运行时当前值）",
				"session", rec.ID, "runtime", spec.ID, "model", rec.ModelID, "err", rerr)
		} else if _, err := as.Projection().ApplyModel(ctx, recCfg); err != nil {
			if ae, ok := acl.As(err); !ok || !strings.HasPrefix(ae.Code, "unsupported_") {
				l.catalog.MarkRejected(spec.ID, recCfg.Model, l.applyReason(err))
			}
			glog.Warnw(ctx, "恢复后重放会话模型失败（忽略，继续用运行时当前值）",
				"session", rec.ID, "runtime", spec.ID, "model", recCfg.Model, "err", err)
		}
	} else if len(as.Projection().ConfigOptions()) > 0 {
		l.catalog.PutConfigOptions(spec.ID, as.Projection().ConfigOptions())
	}
	return as, reason, nil
}

// Attach 把句柄装进活跃集合（替换同 id 的旧句柄），发布 session.resumed。
//
// 被替换的旧句柄：**只作废它自己**（摘 provider 映射 + best-effort 关掉那条原生会话），
// 绝不动它所在的连接——旧代际的暖进程可能还在服务别的会话，代际回收是 M3-7 的事。
// 关掉旧原生会话是为了让底座尽快释放它那份资源（会话级，不是进程级）。
func (l *Lifecycle) Attach(as *ActiveSession, reason string) {
	old := l.handles.Put(as)
	if old != nil && old != as {
		// 旧句柄作废：拿到它旧指针的路径会走重建（而不是用一个已被顶替的会话）
		old.MarkDead(false)
		if c, ok := old.Native().(*acl.Session); ok && c != nil {
			// 关会话是收尾动作：用**脱离请求**的 ctx（本轮 ctx 可能已取消）并带上限等待，
			// 免得底座吞掉 session/close 时把这一轮拖住。0.2.1 起 close 要 drain 后代并
			// flush，窗口用 AgentSettleTimeout（3s 的「发通知」上限不够）。
			cctx, cancel := context.WithTimeout(context.Background(), AgentSettleTimeout)
			_ = c.Close(cctx)
			cancel()
		}
	}
	// §6：执行上下文刚被重建（本次拿到了新的连接/会话）→ 会话执行代际自增。
	// 排队输入比对的就是这个值：重建之后，为旧上下文排的队不再成立。
	l.plane.BumpGeneration(as.ID)
	l.plane.MarkSession(as.ID, SessionStatusInProgress)
	payload := map[string]any{
		"session_id": as.ID, "runtime": as.Runtime, "reason": reason,
	}
	if reason == "generation" {
		// 迁移事件：把**换到了哪一代**带上（审计要把"第 N 轮到第 M 轮用的哪套能力"串起来）。
		payload["capability_fingerprint"] = as.Fingerprint
	}
	l.plane.Publish(BusSessionResumed, as.ID, payload)
}

// ApplyModel 把模型变更应用到**已存在的会话**（会话内改模型）。
//
//   - 会话不在活跃集合（未启动/已断连）→ (false, nil)：调用方只更新元数据，等下次恢复会话时生效；
//   - 活跃 → 经 session/set_config_option 应用（运行时是唯一判据），失败原样上抛（调用方不得吞）。
func (l *Lifecycle) ApplyModel(ctx context.Context, sessionID string, model runtime.ModelConfig) (bool, error) {
	as := l.handles.Get(sessionID)
	if as == nil || as.Dead() || as.Native() == nil {
		return false, nil
	}
	spec, ok := l.registry.Get(as.Runtime)
	if !ok {
		return false, nil
	}
	if err := l.applyModel(ctx, as, spec, model); err != nil {
		return true, err
	}
	return true, nil
}

// applyModel 把模型应用到一条会话上（建会话、恢复后重放与「会话内改模型」共用同一路径）。
//
// 语义（决策记录）：**用户显式指定的取值，运行时要么接受、要么报错**——绝不静默回落默认模型：
//   - 运行时未广告模型项（该底座未配置任何 provider 等）→ model_not_supported；
//   - 运行时拒绝该取值（协议要求值必须来自广告列表）→ 原样上抛并记入「不可用」反馈；
//   - 成功 → 用返回的全量状态刷新平台目录缓存。
//
// model.Model 为空 = 不下发设置（跟随运行时默认），永远成功。
func (l *Lifecycle) applyModel(ctx context.Context, as *ActiveSession, spec runtime.Spec, model runtime.ModelConfig) error {
	if as.Projection() == nil { // 测试注入的假会话（无投影）：无模型语义
		return nil
	}
	if model.Model == "" {
		return nil // 未指定模型：不下发任何设置（跟随运行时默认）
	}
	// 按凭据过滤目录的底座：缺密钥时它的模型根本不在广告列表里，直接下发只会得到一条
	// 「invalidParams」——用户看不出是缺密钥。平台侧先判一次，给出可执行的出路。
	if spec.ModelCatalogNeedsCredential && model.Provider != "" && l.providerKeyReady != nil {
		if ok, reason := l.providerKeyReady(ctx, model.Provider); !ok {
			return acl.NewError(acl.ClassRequest, "model_credential_missing",
				"[model_credential_missing] 所选模型挂靠的供应商 "+model.Provider+" 没有可用密钥（"+reason+
					"）：该底座不会把它的模型列进会话配置；先到「设置 · 模型」补一条密钥（或把环境变量设好）", nil)
		}
	}
	// 不预判、但留痕：取值不在会话当前广告列表里（缓存滞后/跨会话选择）时记一条，仍然下发由
	// 运行时裁决——协议保证运行时只接受已广告的值，平台侧预判会因快照滞后造成假阴性。
	if !acl.HasConfigValue(as.Projection().ConfigOptions(), acl.CategoryModel, model.Model) {
		glog.Debugw(ctx, "模型取值不在当前广告列表中，交由运行时裁决",
			"session", as.ID, "model", model.Model)
	}
	applied, err := as.Projection().ApplyModel(ctx, model)
	if err == nil {
		l.catalog.ClearRejection(spec.ID, model.Model)
		if len(applied) > 0 {
			l.catalog.PutConfigOptions(spec.ID, applied)
		}
		return nil
	}
	ae, _ := acl.As(err)
	if ae != nil && ae.Code == "unsupported_model" {
		return acl.NewError(acl.ClassRequest, "model_not_supported",
			"[model_not_supported] runtime "+spec.ID+" 未提供模型配置项，无法应用所选模型", nil)
	}
	// 运行时明确拒绝：记录该取值不可用（目录里标灰）后原样上抛。
	l.catalog.MarkRejected(spec.ID, model.Model, l.applyReason(err))
	glog.Warnw(ctx, "apply model rejected by runtime",
		"session", as.ID, "runtime", spec.ID, "model", model.Model, "err", err)
	return err
}

// NoteGenerationFallback 迁移失败的留痕：一条 WARN + 一条 runtime 诊断。
//
// 刻意**不**发事件、**不**改会话状态：用户这一轮照常发出去（可用性优先），"能力还没换过来"
// 是运维/诊断层面的事实，不是会话的终态。
func (l *Lifecycle) NoteGenerationFallback(sessionID, runtimeID, from, to string, cause error) {
	glog.Warnw(context.Background(), "代际迁移失败：回退旧代际继续跑",
		"session", sessionID, "runtime", runtimeID,
		"from", from, "to", to, "err", cause)
	if l.diagnostics == nil {
		return
	}
	ae := acl.NewError(acl.ClassConnection, "generation_switch_failed",
		"迁移到新能力集失败（已回退到旧代际继续会话）："+cause.Error(), nil)
	l.diagnostics.RecordRuntimeError(sessionID, ae, nil)
}
