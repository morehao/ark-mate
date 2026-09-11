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

// forgeMarker 标记目录由本平台物化（删除联动只动带标记目录，绝不动用户自有/他方内容）。
const forgeMarker = ".forge-managed"

// 已实证的物化 format（写入 <root>/<skillID>/SKILL.md；两种写法均保证 YAML frontmatter）：
//
// 2026-09-09 双底座实测（真实实现解析 forge 产物）：
//   - opencode v1.18.29（`opencode debug skill`，隔离 XDG_CONFIG_HOME）：无 frontmatter 的
//     SKILL.md **不加载**；有 name+description 的正常加载；`**/SKILL.md` 递归扫描；
//     name 与目录名不一致/含大写下划线仍加载（文档约束非强制）。
//   - dsh 0.1.2-rc.1（`dsh-skill-filesystem` 真实 provider）：无 frontmatter → 「missing YAML
//     frontmatter」丢弃；只有 name 或只有 description → 「frontmatter requires name and
//     description」丢弃；嵌套 `**/SKILL.md` 不扫描。
//
// 故两种 format 均合成/补全 frontmatter（name=skill id，description 退化 name→id）——
// 原样写入 body 的旧约定在两个底座都不会被加载，属实测证伪，不再保留。
const (
	// FormatSkillMD 目录包 `<id>/SKILL.md`（保证 YAML frontmatter）。
	FormatSkillMD = "SKILL.md"
	// FormatSkillMDFrontmatter 同上（显式写法，语义等价；便于定义文件自解释）。
	FormatSkillMDFrontmatter = "SKILL.md+frontmatter"
)

// renderSkillContent 按 format 生成物化内容；ok=false 表示该 source 不可物化（未实证 format）。
func renderSkillContent(s Skill, format string) (string, bool) {
	switch format {
	case FormatSkillMD, FormatSkillMDFrontmatter:
		return withFrontmatter(s), true
	default:
		return "", false
	}
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

// MaterializeSkill 把平台 skill 物化到目标根目录下该 runtime 的原生位置
// （design doc §5.4/§16.5：目录=平台事实源 → 物化=投影层同步 → 执行=runtime 原生）。
//
// 已实证约定（均写 `<root>/<skillID>/SKILL.md`，内容统一为「保证 YAML frontmatter」）：
//   - `format == "SKILL.md"` / `"SKILL.md+frontmatter"`：合成/补全 frontmatter（见上方实测）；
//   - 其它 format 未实证 → 跳过并告警，不臆造。
//
// root 由调用方决定（部署/运维策略；默认不写用户真实目录，见 §16.5 enable 决策）。
// 物化同时写 forge 归属标记（ReconcileSkills 的删除联动依据）。返回物化文件绝对路径；
// 多次调用幂等（覆盖写）。
// runtime == "" 为「单 root 兼容路径」（取首个已实证 format，不分 runtime）。
func MaterializeSkill(root string, s Skill) (string, error) {
	return MaterializeSkillFor(root, "", s)
}

// MaterializeSkillFor 按 runtime 物化：内容取自该 skill 的 `sources[].runtime == runtime`
// 那一条（C4 多 root 投影：每个 runtime 用自己的源与 format 物化到自己的根目录）。
func MaterializeSkillFor(root, runtime string, s Skill) (string, error) {
	if root == "" {
		return "", fmt.Errorf("skill 物化: root 为空")
	}
	if s.ID == "" {
		return "", fmt.Errorf("skill 物化: skill id 为空")
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
	var content, format string
	var ok bool
	content, format, ok = materializedContentFor(s, runtime)
	if !ok {
		if runtime == "" {
			return "", fmt.Errorf("skill 物化: skill %q 无可物化 source（无已实证 format 源）", s.ID)
		}
		return "", fmt.Errorf("skill 物化: skill %q 无 runtime %q 的物化 source（无已实证 format 源）", s.ID, runtime)
	}
	// 单 root 兼容路径（runtime==""）下，同一 skill 的多个 source 指向同一目标文件
	// （<root>/<id>/SKILL.md）：取首个已实证 format，其余仅告警（避免「写多次/比一次」不一致）。
	// C4 多 root 路径下每个 root 只认自己 runtime 的源，不存在该歧义。
	if runtime == "" {
		for _, src := range s.Sources {
			if src.Format == format {
				continue
			}
			if _, renderable := renderSkillContent(s, src.Format); renderable {
				slog.Warn("skill 物化: 同一目标文件存在多种 format，采用首个已实证 format",
					"skill", s.ID, "used", format, "ignored", src.Format, "runtime", src.Runtime)
			}
		}
	}
	dir := filepath.Join(root, s.ID)
	// §13.4 S3：**拒覆盖非自有目录**。此前这里是裸 `MkdirAll` + `WriteFile`：目标目录若已存在
	// 且不是平台物化的（用户自己建的、别的工具装的），平台会直接往里写文件——同名即覆盖别人的东西。
	// 现在先判定归属：不存在 → 新建；存在且带 `.forge-managed` → 可替换；否则显式拒绝。
	if err := ensureReplaceableDir(dir); err != nil {
		return "", err
	}
	// 归属标记（删除联动依据）：首行 skill id，第二行**逗号分隔的 runtime 集合**（有序）。
	// C6：同一目录可能被多个 runtime 投影（共享 root），集合记录全部投影方——某个 runtime 退出投影时
	// 只从集合里摘除自己，集合空了才删除目录（避免「先投影方退出后目录永久残留」）。
	// 旧目录里的归属集合要先读出来合并（替换是"换内容"，不是"重置归属"）。
	_, owners, _ := readMarker(filepath.Join(dir, forgeMarker))

	// §13.4 S3：staging 目录 → 写全 → 自校验摘要 → 原子 rename 替换。
	// 先写临时目录再整体换名的意义：**agent 永远看不到半成品**（没有一个时刻是"SKILL.md 只写了一半"），
	// 且写失败/校验失败时旧产物原样还在（下次重校再试），不会留下一个空目录让 runtime 报错。
	staging, err := os.MkdirTemp(root, ".forge-stage-*")
	if err != nil {
		return "", fmt.Errorf("skill 物化: 创建 staging 目录失败 %s: %w", root, err)
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
	if err := os.WriteFile(filepath.Join(staging, forgeMarker),
		[]byte(markerContent(s.ID, addRuntime(owners, runtime))), 0o644); err != nil {
		return "", fmt.Errorf("skill 物化: 写 staging 归属标记失败: %w", err)
	}
	prov := Provenance{
		Version: provenanceVersion, SkillID: s.ID, Runtime: runtime, Format: format,
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
//   - 存在且是目录且带 `.forge-managed` 标记 → 平台自有，可以替换；
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
	if _, _, ok := readMarker(filepath.Join(dir, forgeMarker)); !ok {
		return fmt.Errorf("skill 物化: 目标目录 %s 已存在但没有 %s 归属标记（疑似用户自有/他方安装的内容）——"+
			"平台不覆盖它：改名或移走该目录，或换一个物化根", dir, forgeMarker)
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
	backup := dir + ".forge-old"
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

// markerContent 生成归属标记内容：首行 skill id，第二行逗号分隔的 runtime 集合（空 = 单 root 兼容路径）。
func markerContent(skillID string, runtimes []string) string {
	return skillID + "\n" + strings.Join(runtimes, ",") + "\n"
}

// addRuntime 把 runtime 并入归属集合（去重 + 排序；空 runtime 不入集合——单 root 兼容路径不登记归属方）。
func addRuntime(owners []string, runtime string) []string {
	if runtime == "" {
		return owners
	}
	for _, o := range owners {
		if o == runtime {
			return owners
		}
	}
	out := append(append([]string{}, owners...), runtime)
	sort.Strings(out)
	return out
}

// removeRuntime 从归属集合摘除 runtime（返回剩余集合与是否发生摘除）。
func removeRuntime(owners []string, runtime string) ([]string, bool) {
	out := make([]string, 0, len(owners))
	removed := false
	for _, o := range owners {
		if o == runtime {
			removed = true
			continue
		}
		out = append(out, o)
	}
	return out, removed
}

// markerHas 报告归属集合是否含该 runtime。
func markerHas(owners []string, runtime string) bool {
	for _, o := range owners {
		if o == runtime {
			return true
		}
	}
	return false
}

// readMarker 解析归属标记（首行 skill id，第二行逗号分隔的 runtime 集合）；文件缺失/为空 → ok=false。
// 兼容旧格式（第二行单个 runtime）：解析结果同为单元素集合。
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

// skillUpToDate 报告 <root>/<id> 是否已按当前内容物化、且归属集合已登记该 runtime（重校幂等判定）。
// runtime=="" 只校验「标记存在 + 内容一致」（单 root 兼容路径不登记归属方）；
// runtime!="" 还要求归属集合含该 runtime——否则本次投影要补登记（共享 root 下各 runtime 各自登记一次，
// 登记完成后即幂等，不会互相改写）。
//
// §13.4 S2 起还要求**摘要自洽**：sidecar 在且记的是同一个 skill、记录摘要 == 期望载荷摘要、
// 磁盘实测摘要 == 记录摘要。只比 SKILL.md 内容是不够的——多塞一个文件、改权限、把 SKILL.md 换成软链，
// 内容比对全都看不出来，而摘要全都看得出来。任一不符 → 判为"需要重校"，下一次重校即修复。
func skillUpToDate(dir string, s Skill, runtime, content, format string) bool {
	_, owners, ok := readMarker(filepath.Join(dir, forgeMarker))
	if !ok {
		return false
	}
	if runtime != "" && !markerHas(owners, runtime) {
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
	// 记录**不参与判定**——共享 root 下同一个目录会被多个 runtime 各投影一次，最后写入者是谁
	// 无关紧要（归属集合由 `.forge-managed` 标记维护，内容一致性由摘要维护）。若把它们纳入判定，
	// 共享 root 的两个 runtime 会互相把对方写的产物判成"过期"而反复重写（幂等性丢失）。
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

// ReconcileSkills 单 root 兼容路径（runtime==""）：root 与全量注册表一致。
func ReconcileSkills(root string, current []Skill) (written, removed int, err error) {
	return ReconcileSkillsFor(root, "", current)
}

// ReconcileSkillsFor 使 <root> 与「声明了该 runtime 的 skill」一致（C4 多 root 投影；§16.5 删除联动 + 启动重校）：
//   - 为注册表里「声明了该 runtime 且含已实证 format 源」的 skill 写缺失/更新（内容一致则跳过，幂等计数）；
//     runtime=="" 时不做 runtime 过滤（单 root 兼容路径）；
//   - 删除 root 下「带 .forge-managed 标记、标记 runtime 属于本次投影范围、且不在本次投影集合」的目录
//     （平台 skill 移除/runtime 声明移除的联动清理）；标记 runtime 决定归属：共享 root 下
//     「目录归属首个物化它的 runtime」，别的 runtime 的重校不会删它（推荐按 runtime 分 root）；
//   - 未带标记的目录（用户自有/他方内容）绝不动；共享 root 下不会删除别的 runtime 的目录。
//
// 返回 (written, removed)。单条失败告警继续；root 空或不存在直接返回零值。
func ReconcileSkillsFor(root, runtime string, current []Skill) (written, removed int, err error) {
	if root == "" {
		return 0, 0, nil
	}
	// 禁用的 skill 不进「本次投影集合」：既不会被写，已物化的副本还会在下面的删除联动里被摘掉
	// （标记目录 + 不在集合 → 删）——「关掉一个技能」在 runtime 目录里是**撤回**，不是留个空壳。
	currentIDs := make(map[string]bool, len(current))
	for _, sk := range current {
		if !sk.IsEnabled() {
			continue
		}
		if _, _, ok := materializedContentFor(sk, runtime); !ok {
			continue
		}
		currentIDs[sk.ID] = true
	}
	for _, sk := range current {
		if !sk.IsEnabled() {
			continue
		}
		content, format, ok := materializedContentFor(sk, runtime)
		if !ok {
			continue
		}
		dir := filepath.Join(root, sk.ID)
		if skillUpToDate(dir, sk, runtime, content, format) {
			continue
		}
		if _, mErr := MaterializeSkillFor(root, runtime, sk); mErr != nil {
			slog.Warn("skill 物化跳过", "skill", sk.ID, "runtime", runtime, "err", mErr)
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
		// 不清扫的话：一个失败的替换会在 root 里永久躺一个 `.forge-old`（它没有归属标记，
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
		marker := filepath.Join(root, e.Name(), forgeMarker)
		skillID, owners, mOK := readMarker(marker)
		if !mOK {
			continue // 无归属标记：用户自有/他方目录，不动
		}
		// 单 root 兼容路径（runtime==""）：本路径拥有该 root，整目录删除联动。
		// 多 root 路径：只摘除自己在归属集合里的登记，集合空了才删目录（共享 root 下不误删他方产物）。
		if runtime != "" {
			if !markerHas(owners, runtime) {
				continue
			}
			rest, _ := removeRuntime(owners, runtime)
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

// materializedContent 单 root 兼容路径（runtime==""）：取首个已实证 format。
func materializedContent(s Skill) (content, format string, ok bool) {
	return materializedContentFor(s, "")
}

// materializedContentFor 返回该 skill 物化后的文件内容与所用 format（与 MaterializeSkillFor 的写入内容
// 一致，用于重校幂等判定）：
//   - runtime == ""：取首个已实证 format（单 root 兼容路径，行为同 C4 之前）；
//   - runtime != ""：只认 sources 里该 runtime 的源（多 root 投影：每个 runtime 用自己的 format）。
func materializedContentFor(s Skill, runtime string) (content, format string, ok bool) {
	for _, src := range s.Sources {
		if runtime != "" && src.Runtime != runtime {
			continue
		}
		if c, renderable := renderSkillContent(s, src.Format); renderable {
			return c, src.Format, true
		}
	}
	return "", "", false
}

// MaterializedContentFor 导出包装：报告该 skill 在该 runtime 下会物化成什么（ok=false = 物化不出来）。
// 只做"应该写成什么"的计算，不碰磁盘——审计视图与重校判定共用同一份逻辑，避免两处漂移。
func MaterializedContentFor(s Skill, runtime string) (content, format string, ok bool) {
	return materializedContentFor(s, runtime)
}

// ManagedDir 报告目录是否由本平台物化（带归属标记）及登记的 runtime 集合。
func ManagedDir(dir string) (owners []string, managed bool) {
	_, owners, ok := readMarker(filepath.Join(dir, forgeMarker))
	return owners, ok
}

// isStagingName 报告目录名是否为平台保留的临时名（staging 目录 / 旧产物备份）。
// 这两个前缀是**平台保留名**：物化根里出现它们即视为平台自己的残留（可安全清扫）。
func isStagingName(name string) bool {
	return strings.HasPrefix(name, ".forge-stage-") || strings.HasSuffix(name, ".forge-old")
}

// hasMaterializableSource 报告 skill 是否含可物化 source（已实证 format；单 root 路径）。
func hasMaterializableSource(s Skill) bool {
	_, _, ok := materializedContent(s)
	return ok
}

// hasMaterializableSourceFor 报告 skill 是否声明了该 runtime 且含可物化 source。
func hasMaterializableSourceFor(s Skill, runtime string) bool {
	_, _, ok := materializedContentFor(s, runtime)
	return ok
}

// RemoveSkillDir 删除 root 下已物化的 skill 目录（幂等；目录不存在视为成功）。
func RemoveSkillDir(root, skillID string) error {
	if root == "" || skillID == "" {
		return nil
	}
	dir := filepath.Join(root, skillID)
	err := os.RemoveAll(dir)
	if err != nil {
		return fmt.Errorf("skill 物化: 删除 %s 失败: %w", dir, err)
	}
	return nil
}
