package capability

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// mateMarker 标记目录由本平台物化（删除联动只动带标记目录，绝不动用户自有/他方内容）。
const mateMarker = ".mate-managed"

// 已实证的物化 format（写入 <root>/<skillID>/SKILL.md；两种写法均保证 YAML frontmatter）：
//
// 2026-09-09 双底座实测（真实实现解析 mate 产物）：
//   - opencode v1.18.29（`opencode debug skill`，隔离 XDG_CONFIG_HOME）：无 frontmatter 的
//     SKILL.md **不加载**；有 name+description 的正常加载；`**/SKILL.md` 递归扫描；
//     name 与目录名不一致/含大写下划线仍加载（文档约束非强制）。
//   - dsh 0.1.2-rc.1（`dsh-skill-filesystem` 真实 provider）：无 frontmatter → 「missing YAML
//     frontmatter」丢弃；只有 name 或只有 description → 「frontmatter requires name and
//     description」丢弃；嵌套 `**/SKILL.md` 不扫描。
//
// 故两种 format 均合成/补全 frontmatter（name=skill id，description 退化 name→id）——
// 原样写入 body 的旧约定在两个底座都不会被加载，属实测证伪，不再保留。
//
// 2026-10-08 补测（**另一条命题，不在上面那段的覆盖范围内**）：「平台默认根下写进去、底座开箱即读到」。
// opencode v1.18.35 通过 → `opencodeSkillProjection().Tested` 已置 true，取证与反证见
// pkg/runtime/adapters.go 该函数上方注释；dsh 侧待补（其 `Tested` 仍为 false）。
// 另：上面「name 含大写下划线仍加载」在 v1.18.35 上复现（`Probe_Bad.Id` 照常加载）——
// 仍是**实现宽容而非契约**，平台未对 skill id 施加 opencode 文档要求的词法闸门。
const (
	// FormatSkillMD 目录包 `<id>/SKILL.md`（保证 YAML frontmatter）。
	FormatSkillMD = "SKILL.md"
	// FormatSkillMDFrontmatter 同上（显式写法，语义等价；便于定义文件自解释）。
	FormatSkillMDFrontmatter = "SKILL.md+frontmatter"
)

// renderSkillContent 按 format 生成物化内容；ok=false 表示该 format 未实证（不臆造）。
func renderSkillContent(s Skill, format string) (string, bool) {
	switch format {
	case FormatSkillMD, FormatSkillMDFrontmatter:
		return withFrontmatter(s), true
	default:
		return "", false
	}
}

// VettedFormats 已实证的 format 列表（稳定顺序；错误文案与激活判定共用）。
func VettedFormats() []string {
	return []string{FormatSkillMD, FormatSkillMDFrontmatter}
}

// withFrontmatter 保证内容带 dsh 要求的 YAML frontmatter（name/description 必填）：
// body 已带 frontmatter 则只补缺失的必填键（用户内容保留），否则前置一段。
// description 取 skill.Description，空则退化为 name/id（dsh 缺 description 会丢弃整条）。
func withFrontmatter(s Skill) string {
	desc := s.Description
	if desc == "" {
		desc = s.Name
	}
	if desc == "" {
		desc = s.ID
	}
	name := s.ID
	body := s.Body
	if fm, rest, ok := splitFrontmatter(body); ok {
		if !hasFrontmatterKey(fm, "name") {
			fm += "name: " + yamlScalar(name) + "\n"
		}
		if !hasFrontmatterKey(fm, "description") {
			fm += "description: " + yamlScalar(desc) + "\n"
		}
		return "---\n" + fm + "---\n" + strings.TrimPrefix(rest, "\n")
	}
	return "---\nname: " + yamlScalar(name) + "\ndescription: " + yamlScalar(desc) + "\n---\n" + strings.TrimPrefix(body, "\n")
}

// splitFrontmatter 切出 YAML frontmatter 与其余正文（无 frontmatter 时 ok=false）。
func splitFrontmatter(body string) (fm, rest string, ok bool) {
	if !strings.HasPrefix(body, "---\n") {
		return "", "", false
	}
	idx := strings.Index(body[len("---\n"):], "\n---")
	if idx < 0 {
		return "", "", false
	}
	start := len("---\n")
	end := start + idx
	rest = body[end+len("\n---"):]
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	} else {
		rest = ""
	}
	return body[start:end] + "\n", rest, true
}

// hasFrontmatterKey 报告 frontmatter 是否已含某顶层键（行首 `key:`）。
func hasFrontmatterKey(fm, key string) bool {
	for _, line := range strings.Split(fm, "\n") {
		if strings.HasPrefix(strings.TrimRight(line, " \t"), key+":") {
			return true
		}
	}
	return false
}

// yamlScalar 输出安全的 YAML 标量（双引号包裹 + 转义；换行折为空格）。
func yamlScalar(v string) string {
	v = strings.ReplaceAll(strings.ReplaceAll(v, "\r", " "), "\n", " ")
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// MaterializeSpec 一次物化的**平凡形态描述**（P-1 的包边界决定）。
//
// 为什么不收 `runtime.Projection`：`pkg/capability` 是能力数据面（可复用基础设施），
// 不该知道"哪个底座把技能放哪、要什么格式"这件事——那是 runtime 的投影声明。
// 调用方（internal/manager）用 `runtime.Resolve` 解析出形态，再把这三个字符串 + 归属名传进来；
// 本包只负责"在 root 下按 layout 合成 format 的内容并原子落盘"。
type MaterializeSpec struct {
	// Root 已展开的**绝对**根目录（占位符由 runtime 层展开；本层不做任何猜测）。
	Root string
	// Layout 落点模板，须含 `<id>`，且**目录部分**必须含 `<id>`（每个技能独占一个平台可控目录
	// ——`.mate-managed` 归属标记与摘要 sidecar 都要有地方落）。
	Layout string
	// Format 内容合成格式（FormatSkillMD / FormatSkillMDFrontmatter；未实证的取值会报错）。
	Format string
	// Owner 归属名，写进 `.mate-managed` 标记的集合（通常是 runtime id）。共享 root 时
	// 各投影方各登记一次，某个投影方退出时只摘除自己，集合空了才删目录。
	Owner string
}

// DirFor 计算该 spec 下某 skill 的独占目录（落点的父目录）。
// 导出给编排层做审计视图（.mate-managed 标记与摘要 sidecar 都落在这里）。
func (m MaterializeSpec) DirFor(id string) (string, error) {
	rel, err := applyLayout(m.Layout, id)
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Join(m.Root, rel)), nil
}

// FileFor 计算该 spec 下某 skill 的产物文件绝对路径。
func (m MaterializeSpec) FileFor(id string) (string, error) {
	rel, err := applyLayout(m.Layout, id)
	if err != nil {
		return "", err
	}
	return filepath.Join(m.Root, rel), nil
}

// applyLayout 把落点模板实例化为相对路径。
//
// 本包**不 import pkg/runtime**，故这里复刻了 `runtime.ValidateProjectionLayout` /
// `ApplyProjectionLayout` 的判定（那两处是声明期校验，这里是写入期的最后一道闸）。两处规则
// 必须一致：`pkg/capability/skill_materialize_test.go` 里有一条断言把两边的取值钉在一起。
func applyLayout(layout, id string) (string, error) {
	if strings.TrimSpace(layout) == "" {
		return "", fmt.Errorf("skill 物化: 落点模板为空")
	}
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("skill 物化: skill id 为空")
	}
	if strings.ContainsAny(id, `/\\`) || id == "." || id == ".." {
		return "", fmt.Errorf("skill 物化: skill id %q 含路径分隔符或为相对段（会逃出投影根）", id)
	}
	if filepath.IsAbs(layout) {
		return "", fmt.Errorf("skill 物化: 落点模板 %q 不能是绝对路径", layout)
	}
	if !strings.Contains(layout, layoutIDToken) {
		return "", fmt.Errorf("skill 物化: 落点模板 %q 必须含 %s 占位", layout, layoutIDToken)
	}
	for _, seg := range strings.Split(filepath.ToSlash(layout), "/") {
		if seg == ".." {
			return "", fmt.Errorf("skill 物化: 落点模板 %q 不得含 `..`（会逃出投影根）", layout)
		}
	}
	rel := strings.ReplaceAll(layout, layoutIDToken, id)
	if dir := filepath.ToSlash(filepath.Dir(filepath.ToSlash(layout))); dir == "." || !strings.Contains(dir, layoutIDToken) {
		return "", fmt.Errorf("skill 物化: 落点模板 %q 必须在含 %s 的**目录**里（平铺成单个文件的话，"+
			"归属标记与摘要 sidecar 无处可落，删除联动也就认不出平台产物）", layout, layoutIDToken)
	}
	return filepath.FromSlash(rel), nil
}

// layoutIDToken 是落点模板里的 skill id 占位符（与 runtime.LayoutIDToken 同值）。
const layoutIDToken = "<id>"

// MaterializeSkill 把一个 skill 物化到 spec 描述的投影位置
// （design doc §5.4/§16.5：目录=平台事实源 → 物化=投影层同步 → 执行=runtime 原生）。
//
// 写盘顺序是刻意的（§13.4 S3）：staging 目录 → 写全 → 自校验摘要 → 原子 rename 替换。
// **agent 永远看不到半成品**，写失败时旧产物原样还在。
// 返回物化文件绝对路径；多次调用幂等（覆盖写）。
func MaterializeSkill(spec MaterializeSpec, s Skill) (string, error) {
	if spec.Root == "" {
		return "", fmt.Errorf("skill 物化: root 为空")
	}
	if s.ID == "" {
		return "", fmt.Errorf("skill 物化: skill id 为空")
	}
	// 词法闸的同款重复判断（理由同上：本函数是导出 API）。id 会**原样写进 frontmatter 的 `name`**，
	// 而底座对 name 有硬词法（dsh：`^[a-z0-9]+(?:-[a-z0-9]+)*$`，不合规只 warn 就丢弃整条）——
	// 平台这边放行等于「自报 in_sync、底座永不加载」，所以与注册表用同一条规则再拦一次。
	if !skillIDRE.MatchString(s.ID) || len(s.ID) > 64 {
		return "", fmt.Errorf("skill 物化: skill id %q 不符底座词法（须 ^[a-z0-9]+(?:-[a-z0-9]+)*$ 且 ≤64 位）", s.ID)
	}
	if s.Body == "" {
		return "", fmt.Errorf("skill 物化: skill %q body 为空（无事可写）", s.ID)
	}
	// §13.4 S4：禁用 = 名字 overlay，只影响投放，不动定义。物化是投放的最后一米，到这里拒绝。
	if !s.IsEnabled() {
		return "", fmt.Errorf("skill 物化: skill %q 已禁用（enabled:false；改回 enabled:true 可恢复）", s.ID)
	}
	// §13.2 S1 的最后一道闸：注册表入口已经拦过 critical，这里再拦一次是因为本函数是**导出 API**
	// ——调用方可以拿一个字面量 Skill 直接调它（绕过注册表）。物化是「把文本装进 agent」的最后一
	// 米，宁可重复判断，不要在这里少一道。
	if warns := VerifyContent(s.Body); HasCritical(warns) {
		return "", criticalErr(s.ID, warns)
	}
	content, ok := renderSkillContent(s, spec.Format)
	if !ok {
		return "", fmt.Errorf("skill 物化: skill %q 的 format %q 未实证（可用: %s）",
			s.ID, spec.Format, strings.Join(VettedFormats(), "|"))
	}
	dir, err := spec.DirFor(s.ID)
	if err != nil {
		return "", err
	}
	// §13.4 S3：**拒覆盖非自有目录**。此前这里是裸 `MkdirAll` + `WriteFile`：目标目录若已存在
	// 且不是平台物化的（用户自己建的、别的工具装的），平台会直接往里写文件——同名即覆盖别人的东西。
	// 现在先判定归属：不存在 → 新建；存在且带 `.mate-managed` → 可替换；否则显式拒绝。
	if err := ensureReplaceableDir(dir); err != nil {
		return "", err
	}
	// 归属标记（删除联动依据）：首行 skill id，第二行**逗号分隔的投影方集合**（有序）。
	// C6：同一目录可能被多个 runtime 投影（共享 root），集合记录全部投影方——某个投影方退出时
	// 只从集合里摘除自己，集合空了才删除目录（避免「先投影方退出后目录永久残留」）。
	// 旧目录里的归属集合要先读出来合并（替换是"换内容"，不是"重置归属"）。
	_, owners, _ := readMarker(filepath.Join(dir, mateMarker))

	// 投影根是 runtime **声明**的（不是平台猜的），所以替它把目录建出来不算越界；反之若不建，
	// 新机器上「用户建的第一条 skill」会直接卡在下一行——`MkdirTemp(spec.Root, …)` 要求根已存在，
	// 而默认根 `~/.agents/skills` 在从未装过 skill 的机器上根本不存在（2026-10-09 平台端到端实测：
	// 只留一条 WARN、定义进表但 skill 静默不投放）。MkdirAll 对已存在的目录是空操作，也不跟随软链，
	// 所以不会覆盖别人的东西；根一旦存在，下面的 staging/原子替换语义与原来完全一致。
	if err := os.MkdirAll(spec.Root, 0o755); err != nil {
		return "", fmt.Errorf("skill 物化: 创建投影根 %s 失败: %w", spec.Root, err)
	}

	staging, err := os.MkdirTemp(spec.Root, ".mate-stage-*")
	if err != nil {
		return "", fmt.Errorf("skill 物化: 创建 staging 目录失败 %s: %w", spec.Root, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(staging)
		}
	}()

	payload := []payloadFile{{Path: "SKILL.md", Mode: 0o644, Content: []byte(content)}}
	if err := os.WriteFile(filepath.Join(staging, "SKILL.md"), payload[0].Content, 0o644); err != nil {
		return "", fmt.Errorf("skill 物化: 写 staging SKILL.md 失败: %w", err)
	}
	if err := os.WriteFile(filepath.Join(staging, mateMarker),
		[]byte(markerContent(s.ID, addOwner(owners, spec.Owner))), 0o644); err != nil {
		return "", fmt.Errorf("skill 物化: 写 staging 归属标记失败: %w", err)
	}
	prov := Provenance{
		Version: provenanceVersion, SkillID: s.ID, Runtime: spec.Owner, Format: spec.Format,
		Hash: hashPayload(payload), Bytes: payloadBytes(content), WrittenAt: time.Now().UTC(),
	}
	if err := WriteProvenance(staging, prov); err != nil {
		return "", err
	}
	// 自校验：刚写完的 staging 重算摘要必须等于记录的摘要（等于把"我以为我写了什么"变成可判定事实；
	// 这一步还能顺带拦住"写入路径其实是个软链"这类意外）。
	got, err := ComputeDirHash(staging)
	if err != nil {
		return "", err
	}
	if got != prov.Hash {
		return "", &HashMismatchError{Expected: prov.Hash, Actual: got}
	}

	if err := replaceDir(staging, dir); err != nil {
		return "", err
	}
	committed = true
	return filepath.Join(dir, "SKILL.md"), nil
}

// ensureReplaceableDir 判定物化目标是否可写：
//   - 不存在（或空路径）→ 可以；
//   - 存在且是目录且带 `.mate-managed` 标记 → 平台自有，可以替换；
//   - 存在但**不是**目录 / 没有归属标记 → 拒绝（别人的东西，平台不动；错误里给出去路）。
func ensureReplaceableDir(dir string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("skill 物化: 检查目标目录 %s 失败: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("skill 物化: 目标 %s 是软链（拒绝对软链路径写入：链接目标可能在物化根之外）", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("skill 物化: 目标 %s 已存在且不是目录（平台不覆盖非自有内容）", dir)
	}
	if _, _, ok := readMarker(filepath.Join(dir, mateMarker)); !ok {
		return fmt.Errorf("skill 物化: 目标目录 %s 已存在但没有 %s 归属标记（疑似用户自有/他方安装的内容）——"+
			"平台不覆盖它：改名或移走该目录，或换一个物化根", dir, mateMarker)
	}
	return nil
}

// replaceDir 用 staging 原子替换 dir（旧目录先挪开，换名成功后再删；换名失败则原样放回）。
//
// 为什么不能直接 rename 到已存在的目录：POSIX rename 要求目标为空目录，非空即 ENOTEMPTY。
// 所以顺序是「挪开旧的 → 换入新的 → 删旧的」；中间任何一步失败都把旧的挪回来，
// 保证「要么是旧产物、要么是新产物」，不会两者都没有。单操作者本地平台，不加锁。
func replaceDir(staging, dir string) error {
	if _, err := os.Lstat(dir); os.IsNotExist(err) {
		if err := os.Rename(staging, dir); err != nil {
			return fmt.Errorf("skill 物化: 换入新产物失败 %s → %s: %w", staging, dir, err)
		}
		return nil
	}
	backup := dir + ".mate-old"
	_ = os.RemoveAll(backup) // 上次中断留下的残留
	if err := os.Rename(dir, backup); err != nil {
		return fmt.Errorf("skill 物化: 挪开旧产物失败 %s: %w", dir, err)
	}
	if err := os.Rename(staging, dir); err != nil {
		// 换入失败：把旧产物放回原位（尽力而为，失败则报出两处路径）。
		if rErr := os.Rename(backup, dir); rErr != nil {
			return fmt.Errorf("skill 物化: 换入失败 %s 且旧产物回滚失败（旧产物在 %s）: %w", dir, backup, errors.Join(err, rErr))
		}
		return fmt.Errorf("skill 物化: 换入新产物失败 %s: %w", dir, err)
	}
	if err := os.RemoveAll(backup); err != nil {
		slog.Warn("skill 物化: 清理旧产物失败（不影响新产物）", "backup", backup, "err", err)
	}
	return nil
}

// markerContent 生成归属标记内容：首行 skill id，第二行逗号分隔的投影方集合。
func markerContent(skillID string, owners []string) string {
	return skillID + "\n" + strings.Join(owners, ",") + "\n"
}

func addOwner(owners []string, owner string) []string {
	if owner == "" {
		return owners
	}
	for _, o := range owners {
		if o == owner {
			return owners
		}
	}
	out := append(append([]string{}, owners...), owner)
	sort.Strings(out)
	return out
}

// removeOwner 从归属集合摘除某投影方（返回剩余集合与是否发生摘除）。
func removeOwner(owners []string, owner string) ([]string, bool) {
	out := make([]string, 0, len(owners))
	removed := false
	for _, o := range owners {
		if o == owner {
			removed = true
			continue
		}
		out = append(out, o)
	}
	return out, removed
}

// markerHasOwner 报告归属集合是否含该投影方。
func markerHasOwner(owners []string, owner string) bool {
	for _, o := range owners {
		if o == owner {
			return true
		}
	}
	return false
}

// readMarker 解析归属标记（首行 skill id，第二行逗号分隔的投影方集合）；文件缺失/为空 → ok=false。
// 兼容旧格式（第二行单个投影方）：解析结果同为单元素集合。
func readMarker(path string) (skillID string, owners []string, ok bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", nil, false
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return "", nil, false
	}
	if len(lines) > 1 {
		for _, part := range strings.Split(lines[1], ",") {
			if part = strings.TrimSpace(part); part != "" {
				owners = append(owners, part)
			}
		}
		sort.Strings(owners)
	}
	return strings.TrimSpace(lines[0]), owners, true
}

// skillUpToDate 报告目标目录是否已按当前内容物化、且归属集合已登记该投影方（重校幂等判定）。
// owner 非空时还要求归属集合含它——否则本次投影要补登记（共享 root 下各投影方各自登记一次，
// 登记完成后即幂等，不会互相改写）。
//
// §13.4 S2 起还要求**摘要自洽**：sidecar 在且记的是同一个 skill、记录摘要 == 期望载荷摘要、
// 磁盘实测摘要 == 记录摘要。只比 SKILL.md 内容是不够的——多塞一个文件、改权限、把 SKILL.md 换成软链，
// 内容比对全都看不出来，而摘要全都看得出来。任一不符 → 判为"需要重校"，下一次重校即修复。
func skillUpToDate(dir string, s Skill, owner, content string) bool {
	_, owners, ok := readMarker(filepath.Join(dir, mateMarker))
	if !ok {
		return false
	}
	if owner != "" && !markerHasOwner(owners, owner) {
		return false
	}
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil || string(b) != content {
		return false
	}
	p, err := ReadProvenance(dir)
	if err != nil {
		return false // 无 sidecar（老版本产物）→ 重校升级一次
	}
	// SkillID 必须对得上（目录名 X 里躺着 Y 的摘要 = 真实的不一致）；Runtime/Format 只作审计
	// 记录**不参与判定**——共享 root 下同一个目录会被多个投影方各投影一次，最后写入者是谁
	// 无关紧要（归属集合由 `.mate-managed` 标记维护，内容一致性由摘要维护）。若把它们纳入判定，
	// 共享 root 的两个投影方会互相把对方写的产物判成"过期"而反复重写（幂等性丢失）。
	if p.SkillID != s.ID {
		return false
	}
	expected := PayloadHash(content)
	if p.Hash != expected {
		return false
	}
	actual, err := ComputeDirHash(dir)
	return err == nil && actual == expected
}

// ReconcileSkills 使投影目标与「投放到该目标的能力集」一致（§16.5 删除联动 + 启动重校）：
//   - 为注册表里「投放到本 owner 且 format 可合成」的 skill 写缺失/更新（内容一致则跳过，幂等计数）；
//   - 删除 root 下「带 .mate-managed 标记、标记含本 owner、且不在本次投影集合」的目录
//     （平台 skill 移除 / 投放声明移除 / 技能被禁用 的联动清理）；共享 root 下只摘除自己在归属
//     集合里的登记，集合空了才删目录（不误删他方产物）；
//   - 未带标记的目录（用户自有/他方内容）绝不动。
//
// 返回 (written, removed)。单条失败告警继续；root 空或不存在直接返回零值。
func ReconcileSkills(spec MaterializeSpec, current []Skill) (written, removed int, err error) {
	root := spec.Root
	if root == "" {
		return 0, 0, nil
	}
	if _, ok := renderSkillContent(Skill{ID: "probe"}, spec.Format); !ok {
		return 0, 0, fmt.Errorf("skill 物化: format %q 未实证（可用: %s）",
			spec.Format, strings.Join(VettedFormats(), "|"))
	}
	// 禁用的 skill 不进「本次投影集合」：既不会被写，已物化的副本还会在下面的删除联动里被摘掉
	// （标记目录 + 不在集合 → 删）——「关掉一个技能」在 runtime 目录里是**撤回**，不是留个空壳。
	currentIDs := make(map[string]bool, len(current))
	contents := make(map[string]string, len(current))
	for _, sk := range current {
		if !sk.IsEnabled() || !sk.AppliesToRuntime(spec.Owner) {
			continue
		}
		content, ok := renderSkillContent(sk, spec.Format)
		if !ok {
			continue
		}
		currentIDs[sk.ID] = true
		contents[sk.ID] = content
	}
	for _, sk := range current {
		content, ok := contents[sk.ID]
		if !ok {
			continue
		}
		dir, dErr := spec.DirFor(sk.ID)
		if dErr != nil {
			slog.Warn("skill 物化跳过", "skill", sk.ID, "owner", spec.Owner, "err", dErr)
			continue
		}
		if skillUpToDate(dir, sk, spec.Owner, content) {
			continue
		}
		if _, mErr := MaterializeSkill(spec, sk); mErr != nil {
			slog.Warn("skill 物化跳过", "skill", sk.ID, "owner", spec.Owner, "err", mErr)
			continue
		}
		written++
	}
	entries, rErr := os.ReadDir(root)
	if rErr != nil {
		if os.IsNotExist(rErr) {
			return written, 0, nil
		}
		return written, 0, fmt.Errorf("skill 物化: 扫描 root 失败 %s: %w", root, rErr)
	}
	for _, e := range entries {
		// 平台保留名：staging 临时目录与替换时的旧产物备份。崩溃/断电可能把它们留下，
		// 这里做一次清扫（它们只在物化根里出现，且**只有平台会造这种名字**，见 replaceDir）。
		// 不清扫的话：一个失败的替换会在 root 里永久躺一个 `.mate-old`（它没有归属标记，
		// 删除联动认不出来，永远没人管）。
		if isStagingName(e.Name()) {
			if e.IsDir() {
				if rmErr := os.RemoveAll(filepath.Join(root, e.Name())); rmErr != nil {
					slog.Warn("skill 物化: 清理 staging 残留失败", "dir", e.Name(), "err", rmErr)
				} else {
					slog.Info("skill 物化: 清理 staging 残留", "dir", e.Name())
				}
			}
			continue
		}
		if !e.IsDir() || currentIDs[e.Name()] {
			continue
		}
		marker := filepath.Join(root, e.Name(), mateMarker)
		skillID, owners, mOK := readMarker(marker)
		if !mOK {
			continue // 无归属标记：用户自有/他方目录，不动
		}
		// 只摘除自己在归属集合里的登记，集合空了才删目录（共享 root 下不误删他方产物）。
		// `owners` 为空 = 旧版单 root 产物（标记第二行空）或无主的平台残留：它是**平台自己写的**，
		// 却没有任何投影方认领——留着只会永久占位，且平台有能力识别它，故直接清理。
		if spec.Owner != "" && len(owners) > 0 {
			if !markerHasOwner(owners, spec.Owner) {
				continue
			}
			rest, _ := removeOwner(owners, spec.Owner)
			if len(rest) > 0 {
				if wErr := os.WriteFile(marker, []byte(markerContent(skillID, rest)), 0o644); wErr != nil {
					slog.Warn("skill 物化: 更新归属标记失败", "dir", e.Name(), "err", wErr)
				}
				continue // 仍被他方投影：保留目录，仅摘除自己的登记
			}
		}
		if dErr := os.RemoveAll(filepath.Join(root, e.Name())); dErr != nil {
			slog.Warn("skill 物化: 删除联动失败", "dir", e.Name(), "err", dErr)
			continue
		}
		removed++
	}
	return written, removed, nil
}

// MaterializedContent 报告该 skill 在给定 format 下会物化成什么（ok=false = 合成不出来）。
// 只做"应该写成什么"的计算，不碰磁盘——审计视图与重校判定共用同一份逻辑，避免两处漂移。
func MaterializedContent(s Skill, format string) (content string, ok bool) {
	return renderSkillContent(s, format)
}

// ManagedDir 报告目录是否由本平台物化（带归属标记）及登记的投影方集合。
func ManagedDir(dir string) (owners []string, managed bool) {
	_, owners, ok := readMarker(filepath.Join(dir, mateMarker))
	return owners, ok
}

// isStagingName 报告目录名是否为平台保留的临时名（staging 目录 / 旧产物备份）。
// 这两个前缀是**平台保留名**：物化根里出现它们即视为平台自己的残留（可安全清扫）。
func isStagingName(name string) bool {
	return strings.HasPrefix(name, ".mate-stage-") || strings.HasSuffix(name, ".mate-old")
}
